#!/usr/bin/env bash
set -euo pipefail

WEAVIATE_URL=${WEAVIATE_URL:-http://localhost:8080}

echo "Checking Weaviate readiness at ${WEAVIATE_URL} ..."
READY=$(curl -sS "${WEAVIATE_URL}/v1/.well-known/ready" || true)
if [ -z "$READY" ]; then
  echo "WARNING: readiness endpoint did not respond. Continuing anyway."
else
  echo "Ready: $READY"
fi

echo "Fetching existing schema ..."
EXISTING=$(curl -sS "${WEAVIATE_URL}/v1/schema" || true)

HAS_CARD=$(python3 - <<'PY'
import json, os
data = {}
try:
    data = json.loads(os.environ.get('EXISTING','{}'))
except Exception as e:
    print(f"WARN: could not parse existing schema JSON: {e}")
found = False
for cl in data.get('classes', []) or []:
    if cl.get('class') == 'Card':
        found = True
        break
print('yes' if found else 'no')
PY
)

if [ "${HAS_CARD}" = "yes" ]; then
  echo "Class Card already exists. Skipping creation."
  exit 0
fi

echo "Creating class(es) from weaviate/schema.json ..."
python3 - <<'PY'
import json, os, sys, subprocess
base = os.environ.get('WEAVIATE_URL','http://localhost:8080').rstrip('/')
try:
    with open('weaviate/schema.json','r',encoding='utf-8') as f:
        s = json.load(f)
except FileNotFoundError:
    print('ERROR: weaviate/schema.json not found', file=sys.stderr)
    sys.exit(1)
except Exception as e:
    print(f'ERROR: failed to parse schema.json: {e}', file=sys.stderr)
    sys.exit(1)

classes = s.get('classes') or []
if not classes:
    print("ERROR: schema.json has no 'classes' entries", file=sys.stderr)
    sys.exit(1)

def call_curl(args):
    p = subprocess.run(args, capture_output=True, text=True)
    out = p.stdout or ''
    code = None
    if 'HTTP_STATUS:' in out:
        body, _, trailer = out.rpartition('HTTP_STATUS:')
        code = trailer.strip()
    else:
        body = out
    return p.returncode, code, body, (p.stderr or '')

errors = False
for c in classes:
    cname = c.get('class') or ''
    payload = json.dumps(c)
    # Attempt POST /v1/schema/classes
    cmd = [
        'curl','-sS','-H','Content-Type: application/json','-X','POST',
        f"{base}/v1/schema/classes", '-d', payload, '-w', '\nHTTP_STATUS:%{http_code}'
    ]
    rc, code, body, err = call_curl(cmd)
    if code in ('200','201'):
        print(f"Created class {cname} via POST (HTTP {code})")
        continue
    # If 405, try PUT /v1/schema/classes/{class}
    if code == '405':
        put_cmd = [
            'curl','-sS','-H','Content-Type: application/json','-X','PUT',
            f"{base}/v1/schema/classes/{cname}", '-d', payload, '-w', '\nHTTP_STATUS:%{http_code}'
        ]
        rc2, code2, body2, err2 = call_curl(put_cmd)
        if code2 in ('200','201'):
            print(f"Created/updated class {cname} via PUT (HTTP {code2})")
            continue
        # Try PUT full schema at /v1/schema
        full_payload = json.dumps({'classes':[c]})
        put_full = [
            'curl','-sS','-H','Content-Type: application/json','-X','PUT',
            f"{base}/v1/schema", '-d', full_payload, '-w', '\nHTTP_STATUS:%{http_code}'
        ]
        rc3, code3, body3, err3 = call_curl(put_full)
        if code3 in ('200','201'):
            print(f"Updated schema via PUT /v1/schema (HTTP {code3})")
            continue
        # Give up on creation paths; we'll verify existence via GraphQL below
        print(f"WARN: failed to create class {cname} via POST/PUT. POST=HTTP {code}, PUT=HTTP {code2}, PUT /v1/schema=HTTP {code3}", file=sys.stderr)
        print(f"POST_BODY:{body}\nPUT_BODY:{body2}\nPUT_SCHEMA_BODY:{body3}", file=sys.stderr)
        errors = True
        continue
    else:
        print(f"ERROR: failed to create class {cname}: HTTP {code or 'unknown'}\n{body}", file=sys.stderr)
        errors = True

# If there were errors, verify whether the class now exists via GraphQL
if errors:
    gql = '{ Get { Card(limit: 0) { name } } }'
    cmd = [
        'curl','-sS','-H','Content-Type: application/json','-X','POST',
        f"{base}/v1/graphql", '-d', json.dumps({'query': gql}), '-w', '\nHTTP_STATUS:%{http_code}'
    ]
    rc, code, body, err = call_curl(cmd)
    if code in ('200','201'):
        # Class exists (query succeeded even if no data)
        print('Detected Card class via GraphQL. Schema is present; continuing.')
        sys.exit(0)
    else:
        print('ERROR: Card class not detected via GraphQL; manual schema creation may be required.', file=sys.stderr)
        sys.exit(1)
else:
    sys.exit(0)
PY

status=$?
if [ $status -eq 0 ]; then
  echo "Schema applied successfully."
else
  echo "Schema apply completed with errors (see messages above)."
fi
