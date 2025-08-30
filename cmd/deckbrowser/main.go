package main

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strings"
    "time"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/bubbles/spinner"
    "github.com/charmbracelet/bubbles/textinput"
    "github.com/charmbracelet/lipgloss"
    wv "github.com/domano/decktech/pkg/weaviateclient"
)

type cfg struct {
    WeaviateURL string `json:"weaviate_url"`
    K           int    `json:"k"`
    Limit       int    `json:"limit"`
}

func defaultCfg() cfg {
    w := os.Getenv("WEAVIATE_URL")
    if w == "" { w = "http://localhost:8080" }
    return cfg{ WeaviateURL: w, K: 10, Limit: 20 }
}

func loadCfg(path string) cfg { c := defaultCfg(); f, err := os.Open(path); if err != nil { return c }; defer f.Close(); _ = json.NewDecoder(f).Decode(&c); return c }
func saveCfg(path string, c cfg) { _ = os.MkdirAll(filepath.Dir(path), 0o755); tmp := path+".tmp"; f, err := os.Create(tmp); if err != nil { return }; _ = json.NewEncoder(f).Encode(&c); _ = f.Close(); _ = os.Rename(tmp, path) }

type Card struct {
    ID         string
    Name       string
    TypeLine   string
    ManaCost   string
    OracleText string
    Colors     []string
    Image      string
    Distance   float64
    Similarity float64
}

type gqlResp struct { Data json.RawMessage `json:"data"`; Errors []struct{ Message string `json:"message"` } `json:"errors"` }

func gqlDo(ctx context.Context, baseURL, query string) (json.RawMessage, error) {
    endpoint := strings.TrimRight(baseURL, "/") + "/v1/graphql"
    body := map[string]string{"query": query}
    b, _ := json.Marshal(body)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
    if err != nil { return nil, err }
    req.Header.Set("Content-Type", "application/json")
    hc := &http.Client{ Timeout: 15 * time.Second }
    resp, err := hc.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        data, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var wr gqlResp
    if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil { return nil, err }
    if len(wr.Errors) > 0 { return nil, errors.New(wr.Errors[0].Message) }
    return wr.Data, nil
}

func listCards(ctx context.Context, baseURL string, offset, limit int) ([]Card, error) {
    cli := wv.NewClient(baseURL)
    res, err := cli.ListCards(ctx, offset, limit)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ ID:c.ID, Name:c.Name, TypeLine:c.TypeLine, ManaCost:c.ManaCost, OracleText:c.OracleText, Image:c.ImageNormal })
    }
    return out, nil
}

func findByNameLike(ctx context.Context, baseURL, name string, limit int) ([]Card, error) {
    cli := wv.NewClient(baseURL)
    res, err := cli.FindByNameLike(ctx, name, limit)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ ID:c.ID, Name:c.Name, TypeLine:c.TypeLine, ManaCost:c.ManaCost, OracleText:c.OracleText, Image:c.ImageNormal })
    }
    return out, nil
}

func fetchVectorForName(ctx context.Context, baseURL, name string) ([]float64, string, error) {
    cli := wv.NewClient(baseURL)
    return cli.FetchVectorForName(ctx, name)
}

func searchSimilar(ctx context.Context, baseURL string, vector []float64, k int) ([]Card, error) {
    cli := wv.NewClient(baseURL)
    res, err := cli.SearchNearVector(ctx, vector, k)
    if err != nil { return nil, err }
    out := make([]Card, 0, len(res))
    for _, c := range res {
        out = append(out, Card{ ID:c.ID, Name:c.Name, TypeLine:c.TypeLine, ManaCost:c.ManaCost, OracleText:c.OracleText, Image:c.ImageNormal, Distance:c.Distance, Similarity:c.Similarity })
    }
    return out, nil
}

// UI
type mode int
const (
    menu mode = iota
    search
    browse
    results
    details
    config
    loading
)

type model struct {
    cfg     cfg
    cfgPath string
    mode    mode
    spinner spinner.Model
    input   textinput.Model
    status  string
    errMsg  string
    cards   []Card
    selected int
    offset  int
}

func newModel(cfgPath string) model {
    c := loadCfg(cfgPath)
    sp := spinner.New(); sp.Spinner = spinner.Dot
    ti := textinput.New(); ti.Placeholder = "Enter card name"; ti.Prompt = "> "
    return model{ cfg:c, cfgPath: cfgPath, mode: menu, spinner: sp, input: ti, status: "" }
}

func (m model) Init() tea.Cmd { return nil }

type done struct{ fn string; cards []Card; err error }
type setStatus string

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case spinner.TickMsg:
        var cmd tea.Cmd
        m.spinner, cmd = m.spinner.Update(msg)
        return m, cmd
    case tea.KeyMsg:
        switch m.mode {
        case menu:
            switch msg.String() {
            case "q", "ctrl+c": return m, tea.Quit
            case "1": m.mode = search; m.input.Focus(); return m, nil
            case "2": m.mode = browse; return m, m.loadPage(0)
            case "3": m.mode = config; return m, nil
            }
        case search:
            switch msg.String() {
            case "esc": m.mode = menu; return m, nil
            case "enter":
                name := strings.TrimSpace(m.input.Value()); if name == "" { return m, nil }
                m.status = "Searching..."; m.errMsg = ""; m.cards = nil; m.selected = 0
                return m, tea.Batch(m.spinner.Tick, m.doSearch(name))
            default:
                var cmd tea.Cmd
                m.input, cmd = m.input.Update(msg)
                return m, cmd
            }
        case browse, results:
            switch msg.String() {
            case "esc": m.mode = menu; return m, nil
            case "up", "k": if m.selected > 0 { m.selected-- }; return m, nil
            case "down", "j": if m.selected < len(m.cards)-1 { m.selected++ }; return m, nil
            case "n": if m.mode == browse { m.offset += m.cfg.Limit; return m, m.loadPage(m.offset) }
            case "p": if m.mode == browse && m.offset >= m.cfg.Limit { m.offset -= m.cfg.Limit; return m, m.loadPage(m.offset) }
            case "enter":
                if len(m.cards) == 0 { return m, nil }
                sel := m.cards[m.selected]
                // Run similar search from selected
                m.mode = loading; m.status = "Searching similar..."; return m, tea.Batch(m.spinner.Tick, m.doSimilar(sel.Name))
            }
        case config:
            switch msg.String() {
            case "esc": m.mode = menu; return m, nil
            case "enter":
                // toggle K and Limit or save URL – simple cycle for brevity
                if strings.HasPrefix(m.input.Value(), "http") { m.cfg.WeaviateURL = m.input.Value() } else { m.cfg.WeaviateURL = m.input.Value() }
                saveCfg(m.cfgPath, m.cfg); m.mode = menu; return m, nil
            default:
                var cmd tea.Cmd
                m.input, cmd = m.input.Update(msg)
                return m, cmd
            }
        }
    case done:
        if msg.err != nil { m.errMsg = msg.err.Error() }
        switch msg.fn {
        case "search":
            m.cards = msg.cards; m.mode = results; m.status = fmt.Sprintf("Found %d match(es)", len(m.cards))
        case "similar":
            m.cards = msg.cards; m.mode = results; m.status = fmt.Sprintf("Top %d similar", len(m.cards))
        case "page":
            m.cards = msg.cards; m.mode = browse; m.status = fmt.Sprintf("Page offset %d", m.offset)
        }
        return m, nil
    case setStatus:
        m.status = string(msg); return m, nil
    }
    return m, nil
}

func (m model) View() string {
    sb := &strings.Builder{}
    title := lipgloss.NewStyle().Bold(true).Render("DeckTech DB Browser")
    fmt.Fprintln(sb, title)
    switch m.mode {
    case menu:
        fmt.Fprintln(sb, "1) Search by name\n2) Browse list\n3) Config\nq) Quit")
        fmt.Fprintf(sb, "DB: %s | K=%d | Limit=%d\n", m.cfg.WeaviateURL, m.cfg.K, m.cfg.Limit)
    case search:
        fmt.Fprintln(sb, "Search by card name (Enter submits, Esc cancels)")
        fmt.Fprintln(sb, m.input.View())
        if m.status != "" { fmt.Fprintln(sb, m.status) }
        if m.errMsg != "" { fmt.Fprintln(sb, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.errMsg)) }
    case browse:
        fmt.Fprintf(sb, "Browse (offset %d). n/p to page, Enter=Similar, Esc=Back\n", m.offset)
        for i, c := range m.cards {
            cur := "  "; if i == m.selected { cur = "> " }
            line := fmt.Sprintf("%s%s — %s", cur, c.Name, c.TypeLine)
            if i == m.selected { line = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(line) }
            fmt.Fprintln(sb, line)
        }
        if m.status != "" { fmt.Fprintln(sb, m.status) }
        if m.errMsg != "" { fmt.Fprintln(sb, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.errMsg)) }
    case results:
        fmt.Fprintln(sb, "Results (Enter=Similar from selected, Esc=Back)")
        for i, c := range m.cards {
            cur := "  "; if i == m.selected { cur = "> " }
            sim := ""; if c.Similarity > 0 { sim = fmt.Sprintf(" (sim %.3f)", c.Similarity) }
            line := fmt.Sprintf("%s%s — %s%s", cur, c.Name, c.TypeLine, sim)
            if i == m.selected { line = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(line) }
            fmt.Fprintln(sb, line)
        }
        if m.status != "" { fmt.Fprintln(sb, m.status) }
        if m.errMsg != "" { fmt.Fprintln(sb, lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.errMsg)) }
    case loading:
        fmt.Fprintln(sb, m.spinner.View(), "Loading...")
        if m.status != "" { fmt.Fprintln(sb, m.status) }
    case config:
        fmt.Fprintln(sb, "Set Weaviate URL, then Enter to save. Esc cancels.")
        if m.input.Value() == "" { m.input.SetValue(m.cfg.WeaviateURL) }
        fmt.Fprintln(sb, m.input.View())
    }
    return sb.String()
}

func (m model) doSearch(name string) tea.Cmd {
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second); defer cancel()
        // first try exact vector; if not, LIKE finds candidates
        // For search list, we show LIKE matches; selecting one triggers similar search.
        matches, err := findByNameLike(ctx, m.cfg.WeaviateURL, name, m.cfg.Limit)
        return done{ fn:"search", cards: matches, err: err }
    }
}

func (m model) doSimilar(name string) tea.Cmd {
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()
        vec, _, err := fetchVectorForName(ctx, m.cfg.WeaviateURL, name)
        if err != nil { return done{ fn:"similar", err: err } }
        res, err := searchSimilar(ctx, m.cfg.WeaviateURL, vec, m.cfg.K)
        return done{ fn:"similar", cards: res, err: err }
    }
}

func (m model) loadPage(offset int) tea.Cmd {
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second); defer cancel()
        res, err := listCards(ctx, m.cfg.WeaviateURL, offset, m.cfg.Limit)
        return done{ fn:"page", cards: res, err: err }
    }
}

func main() {
    cfgPath := filepath.Join(".decktech", "browser.json")
    m := newModel(cfgPath)
    p := tea.NewProgram(m)
    if _, err := p.Run(); err != nil { fmt.Println("Error:", err); os.Exit(1) }
}
