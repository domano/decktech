package main

import (
    "context"
    "embed"
    "fmt"
    "html/template"
    "math/rand"
    "log"
    "net/http"
    "os"
    "strconv"
    "strings"
    "time"
    client "github.com/domano/decktech/pkg/weaviateclient"
)

//go:embed templates/* assets/*
var webFS embed.FS

type Server struct {
    weaviateURL string
    tpl         *template.Template
    cli         *client.Client
}

type Card struct {
    ID          string
    ScryfallID  string
    Name        string
    TypeLine    string
    ManaCost    string
    CMC         float64
    OracleText  string
    Colors      []string
    ColorID     []string
    Keywords    []string
    Power       string
    Toughness   string
    Set         string
    Collector   string
    Rarity      string
    Layout      string
    ImageNormal string
    Distance    float64
    Similarity  float64
    Legalities  map[string]string
}

type Page struct {
    Title       string
    Query       string
    Cards       []Card
    Card        *Card
    Prints      []Card
    Offset      int
    Limit       int
    HasPrev     bool
    HasNext     bool
    NextOffset  int
    PrevOffset  int
    K           int
    Error       string
}

func main() {
    weaviateURL := os.Getenv("WEAVIATE_URL")
    if weaviateURL == "" {
        weaviateURL = "http://localhost:8080"
    }

    funcMap := template.FuncMap{
        "join": func(ss []string, sep string) string { return strings.Join(ss, sep) },
        "uc":   func(s string) string { return strings.ToUpper(s) },
        "scryfallURL": func(c Card) string {
            if c.Set != "" && c.Collector != "" {
                return fmt.Sprintf("https://scryfall.com/card/%s/%s", c.Set, c.Collector)
            }
            if c.ScryfallID != "" {
                return fmt.Sprintf("https://scryfall.com/card/%s", c.ScryfallID)
            }
            return "https://scryfall.com/"
        },
    }
    tpl := template.Must(template.New("base").Funcs(funcMap).ParseFS(webFS, "templates/*.html"))
    s := &Server{weaviateURL: weaviateURL, tpl: tpl, cli: client.NewClient(weaviateURL)}

    mux := http.NewServeMux()
    mux.Handle("/assets/", http.FileServer(http.FS(webFS)))
    mux.HandleFunc("/", s.handleIndex)
    mux.HandleFunc("/cards", s.handleBrowse)
    mux.HandleFunc("/search", s.handleSearch)
    mux.HandleFunc("/similar", s.handleSimilar)
    mux.HandleFunc("/card", s.handleCard)

    addr := ":8090"
    log.Printf("web browsing server on %s (WEAVIATE_URL=%s)", addr, weaviateURL)
    if err := http.ListenAndServe(addr, logRequest(mux)); err != nil {
        log.Fatal(err)
    }
}

func logRequest(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
    })
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
    defer cancel()
    pool, err := s.findByNameLike(ctx, "Legendary", 400)
    if err != nil { pool = nil }
    picks := make([]Card, 0, 24)
    for _, c := range pool {
        if strings.Contains(c.TypeLine, "Legendary") && strings.Contains(c.TypeLine, "Creature") {
            picks = append(picks, c)
        }
    }
    rand.Seed(time.Now().UnixNano())
    for i := range picks {
        j := rand.Intn(i+1)
        picks[i], picks[j] = picks[j], picks[i]
    }
    if len(picks) > 24 { picks = picks[:24] }
    s.render(w, "index.html", Page{Title: "DeckTech — Browse & Search", Cards: picks})
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
    q := r.URL.Query()
    offset := atoiDefault(q.Get("offset"), 0)
    limit := atoiDefault(q.Get("limit"), 20)
    if limit <= 0 || limit > 100 { limit = 20 }

    ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
    defer cancel()
    cards, err := s.listCards(ctx, offset, limit+1) // fetch one extra to detect next
    if err != nil {
        s.render(w, "browse.html", Page{Title: "Browse", Error: err.Error()})
        return
    }
    hasNext := false
    if len(cards) > limit { cards = cards[:limit]; hasNext = true }
    pg := Page{
        Title:      "Browse",
        Cards:      cards,
        Offset:     offset,
        Limit:      limit,
        HasPrev:    offset > 0,
        HasNext:    hasNext,
        PrevOffset: max(0, offset-limit),
        NextOffset: offset + limit,
    }
    s.render(w, "browse.html", pg)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
    q := strings.TrimSpace(r.URL.Query().Get("q"))
    if q == "" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
    defer cancel()
    res, err := s.findByNameLike(ctx, q, 200)
    if err != nil {
        s.render(w, "results.html", Page{Title: "Search", Query: q, Error: err.Error()})
        return
    }
    res = applyFiltersSort(res, r.URL.Query(), false)
    s.render(w, "results.html", Page{Title: "Search", Query: q, Cards: res})
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
    q := r.URL.Query()
    name := strings.TrimSpace(q.Get("name"))
    id := strings.TrimSpace(q.Get("id"))
    k := atoiDefault(q.Get("k"), 200)
    if k < 200 { k = 200 }
    if k > 500 { k = 500 }
    if name == "" && id == "" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
    defer cancel()
    var vec []float64
    var err error
    if id != "" {
        vec, _, err = s.cli.FetchVectorByScryfallID(ctx, id)
    } else {
        vec, _, err = s.cli.FetchVectorForName(ctx, name)
    }
    if err != nil {
        s.render(w, "results.html", Page{Title: "Similar", Query: coalesce(name, id), Error: err.Error()})
        return
    }
    resC, err := s.cli.SearchNearVector(ctx, vec, k)
    if err != nil {
        s.render(w, "results.html", Page{Title: "Similar", Query: coalesce(name, id), Error: err.Error()})
        return
    }
    cards := make([]Card, 0, len(resC))
    for _, c := range resC {
        cards = append(cards, Card{ID: c.ID, ScryfallID: c.ScryfallID, Name: c.Name, TypeLine: c.TypeLine, ManaCost: c.ManaCost, OracleText: c.OracleText, ImageNormal: c.ImageNormal, Distance: c.Distance, Similarity: c.Similarity})
    }
    cards = applyFiltersSort(cards, r.URL.Query(), true)
    s.render(w, "results.html", Page{Title: "Similar", Query: coalesce(name, id), Cards: cards, K: k})
}

func (s *Server) handleCard(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimSpace(r.URL.Query().Get("id"))
    if id == "" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }
    ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
    defer cancel()
    card, err := s.getCardByScryfallID(ctx, id)
    if err != nil {
        s.render(w, "card.html", Page{Title: "Card", Error: err.Error()})
        return
    }
    // Attempt to load all printings by name (works without oracle_id)
    prints, _ := s.listPrintingsByName(ctx, card.Name, 200)
    s.render(w, "card.html", Page{Title: card.Name, Card: &card, Prints: prints})
}

// Rendering
func (s *Server) render(w http.ResponseWriter, name string, data Page) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
    }
}

func (s *Server) listCards(ctx context.Context, offset, limit int) ([]Card, error) {
    res, err := s.cli.ListCards(ctx, offset, limit)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ID: c.ID, ScryfallID: c.ScryfallID, Name: c.Name, TypeLine: c.TypeLine, ManaCost: c.ManaCost, OracleText: c.OracleText, ImageNormal: c.ImageNormal})
    }
    return out, nil
}

func (s *Server) listPrintingsByName(ctx context.Context, name string, limit int) ([]Card, error) {
    res, err := s.cli.ListPrintingsByName(ctx, name, limit)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ID: c.ID, ScryfallID: c.ScryfallID, Set: c.Set, Collector: c.CollectorNum, Rarity: c.Rarity, ImageNormal: c.ImageNormal})
    }
    // simple lexicographic sort by set then collector number (numeric if possible)
    sortPrints(out)
    return out, nil
}

func sortPrints(cs []Card) {
    // attempt numeric collector ordering
    parseNum := func(s string) (int, bool) {
        n, err := strconv.Atoi(s)
        if err != nil { return 0, false }
        return n, true
    }
    // stable sort: set asc, collector numeric asc if possible, else lex
    for i := 0; i < len(cs)-1; i++ {
        for j := i + 1; j < len(cs); j++ {
            a, b := cs[i], cs[j]
            if a.Set == b.Set {
                an, okA := parseNum(a.Collector)
                bn, okB := parseNum(b.Collector)
                swap := false
                if okA && okB {
                    swap = an > bn
                } else {
                    swap = a.Collector > b.Collector
                }
                if swap { cs[i], cs[j] = cs[j], cs[i] }
            } else if a.Set > b.Set {
                cs[i], cs[j] = cs[j], cs[i]
            }
        }
    }
}

func (s *Server) findByNameLike(ctx context.Context, name string, limit int) ([]Card, error) {
    res, err := s.cli.FindByNameLike(ctx, name, limit)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ID: c.ID, ScryfallID: c.ScryfallID, Name: c.Name, TypeLine: c.TypeLine, ManaCost: c.ManaCost, CMC: c.CMC, Colors: c.Colors, OracleText: c.OracleText, ImageNormal: c.ImageNormal})
    }
    return out, nil
}

// Filters and sorters
func applyFiltersSort(cards []Card, q map[string][]string, isSimilar bool) []Card {
    wantLegendary := qValue(q, "legendary") == "1"
    typeFilter := strings.TrimSpace(qValue(q, "type"))
    colorsStr := strings.ReplaceAll(strings.TrimSpace(qValue(q, "colors")), " ", "")
    var colors []string
    if colorsStr != "" { colors = strings.Split(colorsStr, ",") }
    cmcMin := atoiDefault(qValue(q, "cmc_min"), -1)
    cmcMax := atoiDefault(qValue(q, "cmc_max"), -1)

    out := make([]Card, 0, len(cards))
    for _, c := range cards {
        if wantLegendary && !strings.Contains(c.TypeLine, "Legendary") { continue }
        if typeFilter != "" && !strings.Contains(strings.ToLower(c.TypeLine), strings.ToLower(typeFilter)) { continue }
        if len(colors) > 0 {
            if !containsAllColors(c.Colors, colors) { continue }
        }
        if cmcMin >= 0 && int(c.CMC) < cmcMin { continue }
        if cmcMax >= 0 && int(c.CMC) > cmcMax { continue }
        out = append(out, c)
    }
    sortKey := qValue(q, "sort")
    order := qValue(q, "order")
    if sortKey == "" {
        if isSimilar { sortKey = "similarity" } else { sortKey = "name" }
    }
    desc := (order == "desc" || order == "")
    sortCards(out, sortKey, desc)
    return out
}

func qValue(q map[string][]string, k string) string { if v, ok := q[k]; ok && len(v) > 0 { return v[0] }; return "" }

func containsAllColors(have []string, want []string) bool {
    set := map[string]struct{}{}
    for _, c := range have { set[strings.ToUpper(strings.TrimSpace(c))] = struct{}{} }
    for _, c := range want {
        c = strings.ToUpper(strings.TrimSpace(c))
        if c == "" { continue }
        if _, ok := set[c]; !ok { return false }
    }
    return true
}

func sortCards(cs []Card, key string, desc bool) {
    less := func(i, j int) bool { return false }
    switch key {
    case "cmc":
        less = func(i, j int) bool { if cs[i].CMC == cs[j].CMC { return cs[i].Name < cs[j].Name }; return cs[i].CMC < cs[j].CMC }
    case "name":
        less = func(i, j int) bool { return cs[i].Name < cs[j].Name }
    case "similarity":
        less = func(i, j int) bool { if cs[i].Similarity == cs[j].Similarity { return cs[i].Name < cs[j].Name }; return cs[i].Similarity < cs[j].Similarity }
    default:
        less = func(i, j int) bool { return cs[i].Name < cs[j].Name }
    }
    for i := 1; i < len(cs); i++ {
        j := i
        for j > 0 {
            a, b := j-1, j
            cmp := less(a, b)
            if desc { cmp = !cmp }
            if cmp { break }
            cs[a], cs[b] = cs[b], cs[a]
            j--
        }
    }
}


func (s *Server) getCardByScryfallID(ctx context.Context, scryfallID string) (Card, error) {
    c, err := s.cli.GetCardByScryfallID(ctx, scryfallID)
    if err != nil { return Card{}, err }
    return Card{
        ID: c.ID, ScryfallID: c.ScryfallID, Name: c.Name, TypeLine: c.TypeLine, ManaCost: c.ManaCost, CMC: c.CMC,
        OracleText: c.OracleText, Power: c.Power, Toughness: c.Toughness, Colors: c.Colors, ColorID: c.ColorID,
        Keywords: c.Keywords, Set: c.Set, Collector: c.CollectorNum, Rarity: c.Rarity, Layout: c.Layout,
        ImageNormal: c.ImageNormal, Legalities: c.Legalities,
    }, nil
}

// Helpers
func atoiDefault(s string, def int) int { if s == "" { return def }; i, err := strconv.Atoi(s); if err != nil { return def }; return i }
func max(a, b int) int { if a > b { return a }; return b }
func coalesce(a, b string) string { if a != "" { return a }; return b }
