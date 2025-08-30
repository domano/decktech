package main

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/bubbles/progress"
    "github.com/charmbracelet/bubbles/spinner"
    "github.com/charmbracelet/bubbles/textinput"
    "github.com/charmbracelet/lipgloss"
)

type config struct {
    WeaviateURL   string `json:"weaviate_url"`
    ScryfallJSON  string `json:"scryfall_json"`
    Checkpoint    string `json:"checkpoint"`
    OutDir        string `json:"outdir"`
    Model         string `json:"model"`
    IncludeName   bool   `json:"include_name"`
    BatchSize     int    `json:"batch_size"`
}

func defaultConfig() config {
    w := os.Getenv("WEAVIATE_URL")
    if w == "" { w = "http://localhost:8080" }
    return config{
        WeaviateURL:  w,
        ScryfallJSON: "data/oracle-cards.json",
        Checkpoint:   "data/embedding_progress.json",
        OutDir:       "data",
        Model:        "Alibaba-NLP/gte-modernbert-base",
        IncludeName:  false,
        BatchSize:    1000,
    }
}

func loadConfig(path string) (config, error) {
    c := defaultConfig()
    f, err := os.Open(path)
    if err != nil { return c, err }
    defer f.Close()
    dec := json.NewDecoder(f)
    if err := dec.Decode(&c); err != nil { return c, err }
    return c, nil
}

func saveConfig(path string, c config) error {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { return err }
    tmp := path + ".tmp"
    f, err := os.Create(tmp)
    if err != nil { return err }
    enc := json.NewEncoder(f)
    enc.SetIndent("", "  ")
    if err := enc.Encode(&c); err != nil { _ = f.Close(); return err }
    _ = f.Close()
    return os.Rename(tmp, path)
}

type checkpoint struct {
    NextOffset int    `json:"next_offset"`
    Total      int    `json:"total"`
    LastOut    string `json:"last_batch_out"`
}

func readCheckpoint(path string) (checkpoint, error) {
    var cp checkpoint
    f, err := os.Open(path)
    if err != nil { return cp, err }
    defer f.Close()
    dec := json.NewDecoder(f)
    err = dec.Decode(&cp)
    return cp, err
}

// UI
type viewMode int
const (
    modeMenu viewMode = iota
    modeConfig
    modeRun
)

type menuItem struct { title, desc string }

var menuItems = []menuItem{
    {"Download Scryfall", "Fetch bulk JSON to data/oracle-cards.json"},
    {"Apply Schema", "Create/verify Weaviate Card class"},
    {"Run Single Batch", "Embed + ingest one batch using checkpoint"},
    {"Run Continuous", "Loop batches until completion"},
    {"Clean Embeddings", "Delete local batches/checkpoint and wipe Card class"},
    {"Show Status", "Display checkpoint progress"},
    {"Edit Config", "Update paths and parameters"},
    {"Quit", "Exit the CLI"},
}

type runAction int
const (
    actNone runAction = iota
    actDownload
    actApplySchema
    actSingleBatch
    actContinuous
    actClean
    actShowStatus
)

type model struct {
    cfg         config
    cfgPath     string
    mode        viewMode
    sel         int
    spinner     spinner.Model
    progress    progress.Model
    logs        []string
    running     bool
    action      runAction
    // config inputs
    inputs      []*textinput.Model
    cursor      int
}

func newModel(cfgPath string) model {
    s := spinner.New()
    s.Spinner = spinner.Dot
    p := progress.New(progress.WithDefaultGradient())
    // config inputs setup
    c := defaultConfig()
    if f, err := loadConfig(cfgPath); err == nil { c = f }
    inputs := []*textinput.Model{}
    mk := func(placeholder, val string) *textinput.Model {
        ti := textinput.New()
        ti.Placeholder = placeholder
        ti.SetValue(val)
        return &ti
    }
    inputs = append(inputs, mk("Weaviate URL", c.WeaviateURL))
    inputs = append(inputs, mk("Scryfall JSON", c.ScryfallJSON))
    inputs = append(inputs, mk("Checkpoint path", c.Checkpoint))
    inputs = append(inputs, mk("Out dir", c.OutDir))
    inputs = append(inputs, mk("Model", c.Model))
    inputs = append(inputs, mk("Batch size (int)", fmt.Sprintf("%d", c.BatchSize)))
    inc := textinput.New()
    inc.Placeholder = "Include name (true/false)"
    inc.SetValue(fmt.Sprintf("%v", c.IncludeName))
    inputs = append(inputs, &inc)

    return model{
        cfg: c,
        cfgPath: cfgPath,
        mode: modeMenu,
        spinner: s,
        progress: p,
        inputs: inputs,
    }
}

func (m model) Init() tea.Cmd { return nil }

type logMsg string
type doneMsg struct{ err error }
type tickMsg struct{}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case spinner.TickMsg:
        var cmd tea.Cmd
        m.spinner, cmd = m.spinner.Update(msg)
        return m, cmd
    case tea.KeyMsg:
        switch m.mode {
        case modeMenu:
            switch msg.String() {
            case "ctrl+c", "q":
                return m, tea.Quit
            case "up", "k":
                if m.sel > 0 { m.sel-- }
            case "down", "j":
                if m.sel < len(menuItems)-1 { m.sel++ }
            case "enter":
                return m.startAction(m.sel)
            }
        case modeConfig:
            switch msg.String() {
            case "esc":
                m.mode = modeMenu
                return m, nil
            case "tab", "down":
                m.cursor = (m.cursor + 1) % len(m.inputs)
            case "shift+tab", "up":
                m.cursor = (m.cursor - 1 + len(m.inputs)) % len(m.inputs)
            case "enter":
                // Save config
                m.cfg.WeaviateURL = m.inputs[0].Value()
                m.cfg.ScryfallJSON = m.inputs[1].Value()
                m.cfg.Checkpoint = m.inputs[2].Value()
                m.cfg.OutDir = m.inputs[3].Value()
                m.cfg.Model = m.inputs[4].Value()
                if bs, err := fmt.Sscanf(m.inputs[5].Value(), "%d", &m.cfg.BatchSize); bs == 0 || err != nil {
                    m.cfg.BatchSize = 1000
                }
                m.cfg.IncludeName = strings.ToLower(strings.TrimSpace(m.inputs[6].Value())) == "true"
                _ = saveConfig(m.cfgPath, m.cfg)
                m.mode = modeMenu
                return m, nil
            }
            // forward to focused input
            for i := range m.inputs {
                if i == m.cursor {
                    var cmd tea.Cmd
                    *m.inputs[i], cmd = m.inputs[i].Update(msg)
                    return m, cmd
                }
            }
        case modeRun:
            switch msg.String() {
            case "esc":
                // allow cancel display; processes should respect context
                if !m.running { m.mode = modeMenu }
            }
        }
    case tea.WindowSizeMsg:
        return m, nil
    case logMsg:
        m.logs = append(m.logs, string(msg))
        if len(m.logs) > 1000 { m.logs = m.logs[len(m.logs)-1000:] }
        return m, nil
    case doneMsg:
        prev := m.action
        m.running = false
        if msg.err != nil {
            m.logs = append(m.logs, "ERROR: "+msg.err.Error())
        } else {
            // Auto-return to menu for single-shot actions (and continuous when it completes)
            if prev == actSingleBatch || prev == actApplySchema || prev == actDownload || prev == actShowStatus || prev == actClean || prev == actContinuous {
                m.mode = modeMenu
            }
        }
        m.action = actNone
        return m, nil
    case tickMsg:
        // update progress from checkpoint periodically
        cp, err := readCheckpoint(m.cfg.Checkpoint)
        if err == nil && cp.Total > 0 {
            m.progress.SetPercent(float64(cp.NextOffset) / float64(cp.Total))
        }
        if m.running {
            return m, tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
        }
        return m, nil
    }
    return m, nil
}

func (m model) View() string {
    switch m.mode {
    case modeMenu:
        b := &strings.Builder{}
        title := lipgloss.NewStyle().Bold(true).Render("DeckTech CLI — Import & Batch")
        fmt.Fprintln(b, title)
        fmt.Fprintln(b, "Use ↑/↓ to navigate, Enter to run, q to quit.")
        fmt.Fprintln(b)
        for i, it := range menuItems {
            cursor := "  "
            if m.sel == i { cursor = "> " }
            line := fmt.Sprintf("%s%s — %s", cursor, it.title, it.desc)
            if m.sel == i {
                line = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(line)
            }
            fmt.Fprintln(b, line)
        }
        fmt.Fprintln(b)
        cp, err := readCheckpoint(m.cfg.Checkpoint)
        if err == nil && cp.Total > 0 {
            fmt.Fprintf(b, "Progress: %d / %d (%.1f%%)\n", cp.NextOffset, cp.Total, 100*float64(cp.NextOffset)/float64(cp.Total))
        }
        fmt.Fprintf(b, "Weaviate: %s\n", m.cfg.WeaviateURL)
        return b.String()
    case modeConfig:
        b := &strings.Builder{}
        fmt.Fprintln(b, lipgloss.NewStyle().Bold(true).Render("Edit Config (Enter to save, Esc to cancel)"))
        for i, input := range m.inputs {
            if i == m.cursor { input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")) }
            fmt.Fprintln(b, input.View())
        }
        return b.String()
    case modeRun:
        b := &strings.Builder{}
        head := lipgloss.NewStyle().Bold(true).Render("Running… (Esc returns when finished)")
        fmt.Fprintln(b, head)
        if m.running { fmt.Fprintln(b, m.spinner.View()) }
        // Progress bar + numeric checkpoint
        fmt.Fprintln(b, m.progress.View())
        if cp, err := readCheckpoint(m.cfg.Checkpoint); err == nil && cp.Total > 0 {
            pct := 100 * float64(cp.NextOffset) / float64(cp.Total)
            fmt.Fprintf(b, "Progress: %d / %d (%.1f%%)\n", cp.NextOffset, cp.Total, pct)
        }
        fmt.Fprintln(b)
        // show last ~20 log lines
        start := 0
        if len(m.logs) > 20 { start = len(m.logs)-20 }
        for _, l := range m.logs[start:] {
            fmt.Fprintln(b, l)
        }
        return b.String()
    }
    return ""
}

func (m model) startAction(sel int) (tea.Model, tea.Cmd) {
    switch sel {
    case 0: // download
        m.mode, m.running, m.action = modeRun, true, actDownload
        return m, tea.Batch(m.spinner.Tick, m.runDownload(), tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
    case 1: // apply schema
        m.mode, m.running, m.action = modeRun, true, actApplySchema
        return m, tea.Batch(m.spinner.Tick, m.runApplySchema(), tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
    case 2: // single batch
        m.mode, m.running, m.action = modeRun, true, actSingleBatch
        return m, tea.Batch(m.spinner.Tick, m.runSingleBatch(), tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
    case 3: // continuous
        m.mode, m.running, m.action = modeRun, true, actContinuous
        return m, tea.Batch(m.spinner.Tick, m.runContinuous(), tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
    case 4: // clean embeddings
        m.mode, m.running, m.action = modeRun, true, actClean
        return m, tea.Batch(m.spinner.Tick, m.runClean(), tea.Tick(1*time.Second, func(time.Time) tea.Msg { return tickMsg{} }))
    case 5: // show status
        m.mode = modeRun
        m.running = false
        m.action = actShowStatus
        return m, func() tea.Msg {
            cp, err := readCheckpoint(m.cfg.Checkpoint)
            if err != nil { return logMsg("No checkpoint found") }
            pct := 0.0
            if cp.Total > 0 { pct = 100*float64(cp.NextOffset)/float64(cp.Total) }
            return logMsg(fmt.Sprintf("Progress: %d / %d (%.1f%%)", cp.NextOffset, cp.Total, pct))
        }
    case 6: // edit config
        m.mode = modeConfig
        return m, nil
    case 7:
        return m, tea.Quit
    }
    return m, nil
}

// Commands
func (m model) runDownload() tea.Cmd {
    return func() tea.Msg {
        args := []string{"scripts/download_scryfall.py", "-k", "oracle_cards", "-o", m.cfg.ScryfallJSON}
        return runProcess(args, nil)
    }
}

func (m model) runApplySchema() tea.Cmd {
    return func() tea.Msg {
        args := []string{"scripts/apply_schema.sh"}
        return runProcess(args, nil)
    }
}

func (m model) runSingleBatch() tea.Cmd {
    return func() tea.Msg {
        // embed one batch with current checkpoint/offset
        env := []string{"MODEL=" + m.cfg.Model, "EMBED_QUIET=1"}
        if m.cfg.IncludeName { env = append(env, "INCLUDE_NAME=1") }
        // Build batch path by offset (read before)
        cp, _ := readCheckpoint(m.cfg.Checkpoint)
        offset := cp.NextOffset
        out := filepath.Join(m.cfg.OutDir, fmt.Sprintf("weaviate_batch.offset_%d.json", offset))
        embed := []string{"python3", "scripts/embed_cards.py", "--scryfall-json", m.cfg.ScryfallJSON,
            "--batch-out", out, "--limit", fmt.Sprintf("%d", m.cfg.BatchSize), "--offset", fmt.Sprintf("%d", offset), "--checkpoint", m.cfg.Checkpoint, "--model", m.cfg.Model}
        if m.cfg.IncludeName { embed = append(embed, "--include-name") }
        if msg := runProcess(embed, env); isErr(msg) { return msg }
        ingest := []string{"./scripts/ingest_batch.sh", out, m.cfg.WeaviateURL}
        return runProcess(ingest, nil)
    }
}

func (m model) runContinuous() tea.Cmd {
    return func() tea.Msg {
        env := []string{"MODEL=" + m.cfg.Model, "WEAVIATE_URL=" + m.cfg.WeaviateURL, "OUTDIR=" + m.cfg.OutDir, "CHECKPOINT=" + m.cfg.Checkpoint, "EMBED_QUIET=1"}
        if m.cfg.IncludeName { env = append(env, "INCLUDE_NAME=1") }
        args := []string{"./scripts/embed_batches.sh", m.cfg.ScryfallJSON, fmt.Sprintf("%d", m.cfg.BatchSize)}
        return runProcess(args, env)
    }
}

func (m model) runClean() tea.Cmd {
    return func() tea.Msg {
        env := []string{"WEAVIATE_URL=" + m.cfg.WeaviateURL, "OUTDIR=" + m.cfg.OutDir, "CHECKPOINT=" + m.cfg.Checkpoint}
        args := []string{"./scripts/clean_embeddings.sh"}
        return runProcess(args, env)
    }
}

// Utilities
func isErr(msg tea.Msg) bool {
    if dm, ok := msg.(doneMsg); ok { return dm.err != nil }
    return false
}

func runProcess(args []string, extraEnv []string) tea.Msg {
    if len(args) == 0 { return doneMsg{err: fmt.Errorf("no command") } }
    // first element can be a script path or command
    cmdPath := args[0]
    // set a generous timeout for long-running batches
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    // Build command with context to allow cancellation
    var command *exec.Cmd
    if strings.HasSuffix(cmdPath, ".sh") {
        // Run shell scripts via bash to avoid executable bit issues
        command = exec.CommandContext(ctx, "bash", args...)
    } else if strings.HasSuffix(cmdPath, ".py") {
        command = exec.CommandContext(ctx, "python3", args...)
    } else {
        command = exec.CommandContext(ctx, args[0], args[1:]...)
    }
    command.Env = append(os.Environ(), extraEnv...)
    stdout, _ := command.StdoutPipe()
    stderr, _ := command.StderrPipe()
    if err := command.Start(); err != nil {
        return doneMsg{err: err}
    }
    // stream outputs
    go streamLines(stdout)
    go streamLines(stderr)
    err := command.Wait()
    return doneMsg{err: err}
}

func streamLines(r io.Reader) {
    scanner := bufio.NewScanner(r)
    for scanner.Scan() {
        line := scanner.Text()
        tea.Println(line)
    }
}

func main() {
    cfgPath := filepath.Join(".decktech", "config.json")
    m := newModel(cfgPath)
    p := tea.NewProgram(m, tea.WithAltScreen())
    if _, err := p.Run(); err != nil {
        fmt.Println("Error:", err)
        os.Exit(1)
    }
}
