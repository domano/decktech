package main

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "math"
    "net/http"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"
)

type SimilarRequest struct {
    Names   []string               `json:"names"`
    K       int                    `json:"k"`
    Filters map[string]interface{} `json:"filters,omitempty"`
}

type CardResult struct {
    ID            string   `json:"id"`
    Name          string   `json:"name"`
    TypeLine      string   `json:"type_line"`
    ManaCost      string   `json:"mana_cost"`
    OracleText    string   `json:"oracle_text"`
    Colors        []string `json:"colors"`
    ImageNormal   string   `json:"image_normal"`
    Distance      float64  `json:"distance"`
    Similarity    float64  `json:"similarity"`
}

type graphQLResponse struct {
    Data   json.RawMessage   `json:"data"`
    Errors []graphQLError    `json:"errors"`
}

type graphQLError struct {
    Message string `json:"message"`
}

func main() {
    weaviateURL := os.Getenv("WEAVIATE_URL")
    if weaviateURL == "" {
        weaviateURL = "http://localhost:8080"
    }

    mux := http.NewServeMux()
    mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]string{"weaviate_url": weaviateURL})
    })
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })
    mux.HandleFunc("/similar", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
            return
        }
        var req SimilarRequest
        dec := json.NewDecoder(r.Body)
        if err := dec.Decode(&req); err != nil {
            log.Printf("/similar decode error: %v", err)
            http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
            return
        }
        if len(req.Names) == 0 {
            log.Printf("/similar missing names")
            http.Error(w, "names required", http.StatusBadRequest)
            return
        }
        if req.K <= 0 {
            req.K = 10
        }

        ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
        defer cancel()

        vectors, ids, err := fetchVectorsForNames(ctx, weaviateURL, req.Names)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadGateway)
            return
        }
        if len(vectors) == 0 {
            http.Error(w, "no vectors found for input names", http.StatusNotFound)
            return
        }
        qvec := averageVectors(vectors)

        results, err := searchNearVector(ctx, weaviateURL, qvec, req.K)
        if err != nil {
            log.Printf("/similar search error: %v", err)
            http.Error(w, err.Error(), http.StatusBadGateway)
            return
        }

        // Exclude input IDs from results
        idset := map[string]struct{}{}
        for _, id := range ids {
            idset[id] = struct{}{}
        }
        filtered := make([]CardResult, 0, len(results))
        for _, cr := range results {
            if _, ok := idset[cr.ID]; ok {
                continue
            }
            // Convert cosine distance to similarity (1 - distance)
            cr.Similarity = 1.0 - cr.Distance
            filtered = append(filtered, cr)
        }

        w.Header().Set("Content-Type", "application/json")
        enc := json.NewEncoder(w)
        enc.SetIndent("", "  ")
        _ = enc.Encode(filtered)
    })

    srv := &http.Server{Addr: ":8088", Handler: mux}

    go func() {
        log.Printf("similarity service listening on %s (WEAVIATE_URL=%s)", srv.Addr, weaviateURL)
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            log.Fatalf("server error: %v", err)
        }
    }()

    // graceful shutdown
    stop := make(chan os.Signal, 1)
    signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
    <-stop

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = srv.Shutdown(ctx)
}

func fetchVectorsForNames(ctx context.Context, baseURL string, names []string) ([][]float64, []string, error) {
    vectors := make([][]float64, 0, len(names))
    ids := make([]string, 0, len(names))
    for _, name := range names {
        name = strings.TrimSpace(name)
        if name == "" {
            continue
        }
        vec, id, err := fetchVectorForName(ctx, baseURL, name)
        if err != nil {
            return nil, nil, fmt.Errorf("fetch vector for %q: %w", name, err)
        }
        if len(vec) == 0 {
            continue
        }
        vectors = append(vectors, vec)
        ids = append(ids, id)
    }
    return vectors, ids, nil
}

func fetchVectorForName(ctx context.Context, baseURL, name string) ([]float64, string, error) {
    // GraphQL query to get a Card by exact name with vector
    // Limit 1; return _additional { id vector }
    gql := fmt.Sprintf(`{
  Get {
    Card(where: {path: ["name"], operator: Equal, valueString: %q}, limit: 1) {
      name
      _additional { id vector }
    }
  }
}`, name)
    respData, err := doGraphQL(ctx, baseURL, gql)
    if err != nil {
        return nil, "", err
    }

    // Parse response
    var outer struct {
        Get struct {
            Card []struct {
                Name        string    `json:"name"`
                Additional  struct {
                    ID     string     `json:"id"`
                    Vector []float64  `json:"vector"`
                } `json:"_additional"`
            } `json:"Card"`
        } `json:"Get"`
    }
    if err := json.Unmarshal(respData, &outer); err != nil {
        return nil, "", err
    }
    if len(outer.Get.Card) == 0 {
        // Fallback to LIKE search (case-insensitive contains)
        like := fmt.Sprintf("*%s*", name)
        gql2 := fmt.Sprintf(`{
  Get {
    Card(where: {path: ["name"], operator: Like, valueText: %q}, limit: 1) {
      name
      _additional { id vector }
    }
  }
}`, like)
        resp2, err2 := doGraphQL(ctx, baseURL, gql2)
        if err2 != nil {
            return nil, "", fmt.Errorf("card not found: %s", name)
        }
        var outer2 struct {
            Get struct {
                Card []struct {
                    Name       string   `json:"name"`
                    Additional struct {
                        ID     string    `json:"id"`
                        Vector []float64 `json:"vector"`
                    } `json:"_additional"`
                } `json:"Card"`
            } `json:"Get"`
        }
        if err := json.Unmarshal(resp2, &outer2); err != nil {
            return nil, "", fmt.Errorf("card not found: %s", name)
        }
        if len(outer2.Get.Card) == 0 {
            return nil, "", fmt.Errorf("card not found: %s", name)
        }
        c := outer2.Get.Card[0]
        return c.Additional.Vector, c.Additional.ID, nil
    }
    c := outer.Get.Card[0]
    return c.Additional.Vector, c.Additional.ID, nil
}

func searchNearVector(ctx context.Context, baseURL string, vector []float64, k int) ([]CardResult, error) {
    // Build nearVector JSON array string
    vb, _ := json.Marshal(vector)
    gql := fmt.Sprintf(`{
  Get {
    Card(nearVector: { vector: %s }, limit: %d) {
      name
      type_line
      mana_cost
      oracle_text
      colors
      image_normal
      _additional { id distance }
    }
  }
}`, string(vb), k)
    respData, err := doGraphQL(ctx, baseURL, gql)
    if err != nil {
        return nil, err
    }
    var outer struct {
        Get struct {
            Card []struct {
                Name       string   `json:"name"`
                TypeLine   string   `json:"type_line"`
                ManaCost   string   `json:"mana_cost"`
                OracleText string   `json:"oracle_text"`
                Colors     []string `json:"colors"`
                Image      string   `json:"image_normal"`
                Additional struct {
                    ID       string  `json:"id"`
                    Distance float64 `json:"distance"`
                } `json:"_additional"`
            } `json:"Card"`
        } `json:"Get"`
    }
    if err := json.Unmarshal(respData, &outer); err != nil {
        return nil, err
    }
    res := make([]CardResult, 0, len(outer.Get.Card))
    for _, c := range outer.Get.Card {
        res = append(res, CardResult{
            ID:          c.Additional.ID,
            Name:        c.Name,
            TypeLine:    c.TypeLine,
            ManaCost:    c.ManaCost,
            OracleText:  c.OracleText,
            Colors:      c.Colors,
            ImageNormal: c.Image,
            Distance:    c.Additional.Distance,
        })
    }
    return res, nil
}

func doGraphQL(ctx context.Context, baseURL, query string) (json.RawMessage, error) {
    endpoint := strings.TrimRight(baseURL, "/") + "/v1/graphql"
    body := map[string]string{"query": query}
    b, _ := json.Marshal(body)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    httpClient := &http.Client{Timeout: 10 * time.Second}
    resp, err := httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        data, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var wrapper graphQLResponse
    dec := json.NewDecoder(resp.Body)
    if err := dec.Decode(&wrapper); err != nil {
        return nil, err
    }
    if len(wrapper.Errors) > 0 {
        return nil, fmt.Errorf("graphql error: %s", wrapper.Errors[0].Message)
    }
    return wrapper.Data, nil
}

func averageVectors(vectors [][]float64) []float64 {
    if len(vectors) == 0 {
        return nil
    }
    dim := len(vectors[0])
    out := make([]float64, dim)
    for _, v := range vectors {
        for i := 0; i < dim; i++ {
            out[i] += v[i]
        }
    }
    inv := 1.0 / float64(len(vectors))
    var norm float64
    for i := 0; i < dim; i++ {
        out[i] *= inv
        norm += out[i] * out[i]
    }
    // Normalize to unit length for cosine distance
    norm = math.Sqrt(norm)
    if norm > 0 {
        for i := 0; i < dim; i++ {
            out[i] /= norm
        }
    }
    return out
}
