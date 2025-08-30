package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "log"
    "math"
    "net/http"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"

    client "github.com/domano/decktech/pkg/weaviateclient"
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

        cli := client.NewClient(weaviateURL)
        vectors, ids, err := fetchVectorsForNames(ctx, cli, req.Names)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadGateway)
            return
        }
        if len(vectors) == 0 {
            http.Error(w, "no vectors found for input names", http.StatusNotFound)
            return
        }
        qvec := averageVectors(vectors)

        resultsC, err := cli.SearchNearVector(ctx, qvec, req.K)
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
        filtered := make([]CardResult, 0, len(resultsC))
        for _, c := range resultsC {
            if _, ok := idset[c.ID]; ok {
                continue
            }
            filtered = append(filtered, CardResult{
                ID:          c.ID,
                Name:        c.Name,
                TypeLine:    c.TypeLine,
                ManaCost:    c.ManaCost,
                OracleText:  c.OracleText,
                Colors:      c.Colors,
                ImageNormal: c.ImageNormal,
                Distance:    c.Distance,
                Similarity:  c.Similarity,
            })
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

func fetchVectorsForNames(ctx context.Context, cli *client.Client, names []string) ([][]float64, []string, error) {
    vectors := make([][]float64, 0, len(names))
    ids := make([]string, 0, len(names))
    for _, name := range names {
        name = strings.TrimSpace(name)
        if name == "" {
            continue
        }
        vec, id, err := cli.FetchVectorForName(ctx, name)
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
// Removed raw GraphQL helpers; use pkg/weaviateclient instead.

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
