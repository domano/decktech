package weaviateclient

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "strings"
    "time"
)

// Client is a minimal GraphQL helper for Weaviate focused on the Card class.
// It provides typed helpers used by the REST server, TUIs, and the web app.
type Client struct {
    baseURL string
    http    *http.Client
}

// NewClient creates a new client. baseURL should be like "http://localhost:8080".
func NewClient(baseURL string) *Client {
    return &Client{
        baseURL: strings.TrimRight(baseURL, "/"),
        http:    &http.Client{Timeout: 15 * time.Second},
    }
}

// Card is a union of commonly used card fields. Not all fields will be set in all queries.
type Card struct {
    ID           string            `json:"id"`
    ScryfallID   string            `json:"scryfall_id"`
    Name         string            `json:"name"`
    TypeLine     string            `json:"type_line"`
    ManaCost     string            `json:"mana_cost"`
    CMC          float64           `json:"cmc"`
    OracleText   string            `json:"oracle_text"`
    Power        string            `json:"power"`
    Toughness    string            `json:"toughness"`
    Colors       []string          `json:"colors"`
    ColorID      []string          `json:"color_identity"`
    Keywords     []string          `json:"keywords"`
    Set          string            `json:"set"`
    CollectorNum string            `json:"collector_number"`
    Rarity       string            `json:"rarity"`
    Layout       string            `json:"layout"`
    ImageNormal  string            `json:"image_normal"`
    Distance     float64           `json:"distance"`
    Similarity   float64           `json:"similarity"`
    Legalities   map[string]string `json:"legalities"`
}

type gqlResp struct {
    Data   json.RawMessage `json:"data"`
    Errors []struct {
        Message string `json:"message"`
    } `json:"errors"`
}

// do runs a GraphQL query and returns the raw data payload.
func (c *Client) do(ctx context.Context, query string) (json.RawMessage, error) {
    endpoint := c.baseURL + "/v1/graphql"
    body := map[string]string{"query": query}
    b, _ := json.Marshal(body)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        data, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
    }
    var wr gqlResp
    if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
        return nil, err
    }
    if len(wr.Errors) > 0 {
        return nil, errors.New(wr.Errors[0].Message)
    }
    return wr.Data, nil
}

// FetchVectorForName returns (vector, objectID) for an exact name, with LIKE fallback.
func (c *Client) FetchVectorForName(ctx context.Context, name string) ([]float64, string, error) {
    q := fmt.Sprintf(`{ Get { Card(where:{path:["name"], operator: Equal, valueString:%q}, limit:1){ name _additional{ id vector } } } }`, name)
    data, err := c.do(ctx, q)
    if err != nil {
        return nil, "", err
    }
    var o struct{
        Get struct{
            Card []struct{
                Name string `json:"name"`
                Add  struct{
                    ID     string    `json:"id"`
                    Vector []float64 `json:"vector"`
                } `json:"_additional"`
            } `json:"Card"`
        } `json:"Get"`
    }
    if err := json.Unmarshal(data, &o); err != nil {
        return nil, "", err
    }
    if len(o.Get.Card) == 0 {
        like := fmt.Sprintf("*%s*", name)
        q2 := fmt.Sprintf(`{ Get { Card(where:{path:["name"], operator: Like, valueText:%q}, limit:1){ name _additional{ id vector } } } }`, like)
        d2, err2 := c.do(ctx, q2)
        if err2 != nil {
            return nil, "", fmt.Errorf("card not found: %s", name)
        }
        var o2 struct{
            Get struct{
                Card []struct{
                    Name string `json:"name"`
                    Add  struct{ ID string `json:"id"`; Vector []float64 `json:"vector"` } `json:"_additional"`
                } `json:"Card"`
            } `json:"Get"`
        }
        if err := json.Unmarshal(d2, &o2); err != nil || len(o2.Get.Card) == 0 {
            return nil, "", fmt.Errorf("card not found: %s", name)
        }
        c0 := o2.Get.Card[0]
        return c0.Add.Vector, c0.Add.ID, nil
    }
    c0 := o.Get.Card[0]
    return c0.Add.Vector, c0.Add.ID, nil
}

// SearchNearVector returns the top-k similar cards to a query vector.
func (c *Client) SearchNearVector(ctx context.Context, vector []float64, k int) ([]Card, error) {
    vb, _ := json.Marshal(vector)
    q := fmt.Sprintf(`{ Get { Card(nearVector:{ vector:%s }, limit:%d){ scryfall_id name type_line mana_cost cmc colors set rarity oracle_text image_normal _additional{ id distance } } } }`, string(vb), k)
    data, err := c.do(ctx, q)
    if err != nil {
        return nil, err
    }
    var o struct{
        Get struct{
            Card []struct{
                ScryID string `json:"scryfall_id"`
                Name   string `json:"name"`
                Type   string `json:"type_line"`
                Mana   string `json:"mana_cost"`
                CMC    float64 `json:"cmc"`
                Colors []string `json:"colors"`
                Set    string   `json:"set"`
                Rarity string   `json:"rarity"`
                Oracle string `json:"oracle_text"`
                Img    string `json:"image_normal"`
                Add    struct{ ID string `json:"id"`; Distance float64 `json:"distance"` } `json:"_additional"`
            } `json:"Card"`
        } `json:"Get"`
    }
    if err := json.Unmarshal(data, &o); err != nil {
        return nil, err
    }
    out := make([]Card, 0, len(o.Get.Card))
    for _, c0 := range o.Get.Card {
        sim := 1.0 - c0.Add.Distance
        out = append(out, Card{
            ID: c0.Add.ID, ScryfallID: c0.ScryID, Name: c0.Name, TypeLine: c0.Type, ManaCost: c0.Mana,
            CMC: c0.CMC, Colors: c0.Colors, Rarity: c0.Rarity, Set: c0.Set,
            OracleText: c0.Oracle, ImageNormal: c0.Img, Distance: c0.Add.Distance, Similarity: sim,
        })
    }
    return out, nil
}

// FetchVectorByScryfallID returns (vector, objectID) for a given scryfall_id.
func (c *Client) FetchVectorByScryfallID(ctx context.Context, scryID string) ([]float64, string, error) {
    q := fmt.Sprintf(`{ Get { Card(where:{path:["scryfall_id"], operator: Equal, valueString:%q}, limit:1){ scryfall_id _additional{ id vector } } } }`, scryID)
    data, err := c.do(ctx, q)
    if err != nil { return nil, "", err }
    var o struct{ Get struct{ Card []struct{ Scry string `json:"scryfall_id"`; Add struct{ ID string `json:"id"`; Vector []float64 `json:"vector"` } `json:"_additional"` } `json:"Card"` } `json:"Get"` }
    if err := json.Unmarshal(data, &o); err != nil { return nil, "", err }
    if len(o.Get.Card) == 0 { return nil, "", fmt.Errorf("card not found: %s", scryID) }
    c0 := o.Get.Card[0]
    return c0.Add.Vector, c0.Add.ID, nil
}

// ListCards returns a simple list view for browsing.
func (c *Client) ListCards(ctx context.Context, offset, limit int) ([]Card, error) {
    q := fmt.Sprintf(`{ Get { Card(limit:%d, offset:%d){ scryfall_id name type_line mana_cost cmc colors set rarity oracle_text image_normal _additional{ id } } } }`, limit, offset)
    data, err := c.do(ctx, q)
    if err != nil { return nil, err }
    var outer struct { Get struct { Card []struct {
        Scry string `json:"scryfall_id"`
        Name string `json:"name"`
        Type string `json:"type_line"`
        Mana string `json:"mana_cost"`
        CMC  float64 `json:"cmc"`
        Colors []string `json:"colors"`
        Set   string `json:"set"`
        Rarity string `json:"rarity"`
        Oracle string `json:"oracle_text"`
        Img string `json:"image_normal"`
        Add struct { ID string `json:"id"` } `json:"_additional"`
    } `json:"Card"` } `json:"Get"` }
    if err := json.Unmarshal(data, &outer); err != nil { return nil, err }
    out := make([]Card, 0, len(outer.Get.Card))
    for _, c0 := range outer.Get.Card {
        out = append(out, Card{ID: c0.Add.ID, ScryfallID: c0.Scry, Name: c0.Name, TypeLine: c0.Type, ManaCost: c0.Mana, CMC: c0.CMC, Colors: c0.Colors, Set: c0.Set, Rarity: c0.Rarity, OracleText: c0.Oracle, ImageNormal: c0.Img})
    }
    return out, nil
}

// FindByNameLike returns name-matching cards using LIKE.
func (c *Client) FindByNameLike(ctx context.Context, name string, limit int) ([]Card, error) {
    like := fmt.Sprintf("*%s*", name)
    q := fmt.Sprintf(`{ Get { Card(where:{path:["name"], operator: Like, valueText:%q}, limit:%d){ scryfall_id name type_line mana_cost cmc colors set rarity oracle_text image_normal _additional{ id } } } }`, like, limit)
    data, err := c.do(ctx, q)
    if err != nil { return nil, err }
    var outer struct { Get struct { Card []struct {
        Scry string `json:"scryfall_id"`
        Name string `json:"name"`
        Type string `json:"type_line"`
        Mana string `json:"mana_cost"`
        CMC  float64 `json:"cmc"`
        Colors []string `json:"colors"`
        Set   string `json:"set"`
        Rarity string `json:"rarity"`
        Oracle string `json:"oracle_text"`
        Img string `json:"image_normal"`
        Add struct { ID string `json:"id"` } `json:"_additional"`
    } `json:"Card"` } `json:"Get"` }
    if err := json.Unmarshal(data, &outer); err != nil { return nil, err }
    out := make([]Card, 0, len(outer.Get.Card))
    for _, c0 := range outer.Get.Card {
        out = append(out, Card{ID: c0.Add.ID, ScryfallID: c0.Scry, Name: c0.Name, TypeLine: c0.Type, ManaCost: c0.Mana, CMC: c0.CMC, Colors: c0.Colors, Set: c0.Set, Rarity: c0.Rarity, OracleText: c0.Oracle, ImageNormal: c0.Img})
    }
    return out, nil
}

// GetCardByScryfallID returns a richly populated card for the detail view.
func (c *Client) GetCardByScryfallID(ctx context.Context, scryfallID string) (Card, error) {
    q := fmt.Sprintf(`{ Get { Card(where:{path:["scryfall_id"], operator: Equal, valueString:%q}, limit:1){
      scryfall_id name type_line mana_cost cmc oracle_text power toughness colors color_identity keywords edhrec_rank set collector_number rarity layout legalities image_normal
      _additional{ id }
    } } }`, scryfallID)
    data, err := c.do(ctx, q)
    if err != nil { return Card{}, err }
    var o struct { Get struct { Card []struct {
        Scry   string   `json:"scryfall_id"`
        Name   string   `json:"name"`
        Type   string   `json:"type_line"`
        Mana   string   `json:"mana_cost"`
        CMC    float64  `json:"cmc"`
        Oracle string   `json:"oracle_text"`
        Power  string   `json:"power"`
        Tough  string   `json:"toughness"`
        Colors []string `json:"colors"`
        ColorI []string `json:"color_identity"`
        Keys   []string `json:"keywords"`
        Set    string   `json:"set"`
        Coll   string   `json:"collector_number"`
        Rarity string   `json:"rarity"`
        Layout string   `json:"layout"`
        Legal  string   `json:"legalities"`
        Img    string   `json:"image_normal"`
        Add    struct { ID string `json:"id"` } `json:"_additional"`
    } `json:"Card"` } `json:"Get"` }
    if err := json.Unmarshal(data, &o); err != nil { return Card{}, err }
    if len(o.Get.Card) == 0 { return Card{}, fmt.Errorf("card not found: %s", scryfallID) }
    c0 := o.Get.Card[0]
    leg := map[string]string{}
    if c0.Legal != "" {
        _ = json.Unmarshal([]byte(c0.Legal), &leg)
    }
    return Card{
        ID: c0.Add.ID, ScryfallID: c0.Scry, Name: c0.Name, TypeLine: c0.Type, ManaCost: c0.Mana, CMC: c0.CMC,
        OracleText: c0.Oracle, Power: c0.Power, Toughness: c0.Tough, Colors: c0.Colors, ColorID: c0.ColorI,
        Keywords: c0.Keys, Set: c0.Set, CollectorNum: c0.Coll, Rarity: c0.Rarity, Layout: c0.Layout,
        ImageNormal: c0.Img, Legalities: leg,
    }, nil
}

// ListPrintingsByName returns different printings (same name) with set/collector info.
func (c *Client) ListPrintingsByName(ctx context.Context, name string, limit int) ([]Card, error) {
    q := fmt.Sprintf(`{ Get { Card(where:{path:["name"], operator: Equal, valueString:%q}, limit:%d){ scryfall_id set collector_number rarity image_normal _additional{ id } } } }`, name, limit)
    data, err := c.do(ctx, q)
    if err != nil { return nil, err }
    var outer struct { Get struct { Card []struct {
        Scry string `json:"scryfall_id"`
        Set  string `json:"set"`
        Coll string `json:"collector_number"`
        Rar  string `json:"rarity"`
        Img  string `json:"image_normal"`
        Add  struct{ ID string `json:"id"` } `json:"_additional"`
    } `json:"Card"` } `json:"Get"` }
    if err := json.Unmarshal(data, &outer); err != nil { return nil, err }
    out := make([]Card, 0, len(outer.Get.Card))
    for _, c0 := range outer.Get.Card {
        out = append(out, Card{ID: c0.Add.ID, ScryfallID: c0.Scry, Set: c0.Set, CollectorNum: c0.Coll, Rarity: c0.Rar, ImageNormal: c0.Img})
    }
    return out, nil
}
