package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var mockUsage = json.RawMessage(`{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`)

func serveTokenCount(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, "/input_tokens") {
		return false
	}
	fmt.Fprint(w, `{"input_tokens":1000}`)
	return true
}

func TestSessionCost(t *testing.T) {
	tracker, err := newCostTracker(ProviderConfig{})
	if err != nil || tracker.Cost().LimitUSD != 0.50 {
		t.Fatalf("default: %+v, %v", tracker, err)
	}
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		if tracker.SetCostLimitUSD(v) == nil {
			t.Fatalf("accepted limit %v", v)
		}
	}
	var usage TokenUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":300,"cache_write_tokens":400},"output_tokens_details":{"reasoning_tokens":150}}`), &usage); err != nil {
		t.Fatal(err)
	}
	got, err := usageCost(usage)
	// 300 ordinary + 300 cached + 400 writes; reasoning included in 200 output.
	if err != nil || math.Abs(got-0.00363) > 1e-12 {
		t.Fatalf("cost=%v, err=%v", got, err)
	}
	usage.InputTokens = 300000
	got, err = usageCost(usage)
	want := float64(300000-700)*4e-6 + 300*0.2e-6 + 400*5e-6 + 200*15e-6
	if err != nil || math.Abs(got-want) > 1e-12 {
		t.Fatalf("long context cost=%v, want %v, err=%v", got, want, err)
	}
	tracker.cost.SpentUSD = 0.1
	if err := tracker.SetCostLimitUSD(0); err != nil {
		t.Fatal(err)
	}
	if tracker.Cost().SpentUSD != 0.1 {
		t.Fatal("changing allowance reset spend")
	}
	if _, _, err := tracker.outputAllowance(0); err == nil {
		t.Fatal("zero limit allowed request")
	}
	if err := tracker.SetCostLimitUSD(0.5); err != nil {
		t.Fatal(err)
	}
	tokens, reserve, err := tracker.outputAllowance(10000)
	if err != nil || tokens < 16 || reserve+tracker.Cost().SpentUSD > 0.5 {
		t.Fatalf("invalid reservation: %d %v %v", tokens, reserve, err)
	}
}

func TestOpenAICostGuard(t *testing.T) {
	for _, scenario := range []string{"exhausted", "input too large", "missing usage", "bad count", "zero", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			requests, counts := 0, 0
			limit := 0.01
			if scenario == "zero" {
				limit = 0
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/input_tokens") {
					counts++
					if scenario == "bad count" {
						fmt.Fprint(w, `{}`)
						return
					}
					if scenario == "input too large" {
						fmt.Fprint(w, `{"input_tokens":100000}`)
						return
					}
					fmt.Fprint(w, `{"input_tokens":1000}`)
					return
				}
				requests++
				var body struct {
					MaxOutput int               `json:"max_output_tokens"`
					Tier      string            `json:"service_tier"`
					Cache     map[string]string `json:"prompt_cache_options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.MaxOutput < 16 || 0.0025+float64(body.MaxOutput)*10e-6 > limit || body.Tier != "default" || body.Cache["ttl"] != "30m" {
					t.Errorf("unbounded or incorrectly priced request: %+v", body)
				}
				args, _ := json.Marshal(map[string]string{"description": "Print one value.", "service": "", "script": "print(42)"})
				reply := map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "name": "exec", "call_id": "call_1", "arguments": string(args)}}}
				if scenario != "missing usage" {
					reply["usage"] = map[string]any{"input_tokens": 1000, "output_tokens": body.MaxOutput, "total_tokens": 1000 + body.MaxOutput, "input_tokens_details": map[string]int{"cached_tokens": 0, "cache_write_tokens": 1000}}
				}
				if scenario == "incomplete" {
					reply["status"] = "incomplete"
					reply["incomplete_details"] = map[string]string{"reason": "max_output_tokens"}
				}
				json.NewEncoder(w).Encode(reply)
			}))
			defer api.Close()
			server := sessionTestServer(t)
			server.providers["openai"] = &openAIProvider{key: "test", endpoint: api.URL, client: api.Client()}
			var input sessionInput
			if err := readSessionFile("../../tests/inputs/hello.yml", &input); err != nil {
				t.Fatal(err)
			}
			input.Services = nil
			input.Provider.MaxCostUSD = &limit
			var output bytes.Buffer
			if err := input.run(t.Context(), server, &output); err == nil {
				t.Fatal("budget did not stop session")
			}
			expected := 1
			if scenario == "input too large" || scenario == "zero" || scenario == "bad count" {
				expected = 0
			}
			if requests != expected || (scenario == "zero" && counts != 0) {
				t.Fatalf("paid=%d, counts=%d", requests, counts)
			}
			var log sessionLog
			if err := yaml.Unmarshal(output.Bytes(), &log); err != nil {
				t.Fatal(err)
			}
			last := log.Messages[len(log.Messages)-1]
			if last.Kind != "error" || last.Cost == nil || last.Cost.LimitUSD != limit || last.Cost.SpentUSD > limit {
				t.Fatalf("bad final cost: %+v", last)
			}
			if scenario == "missing usage" && !last.Cost.Estimated {
				t.Fatal("missing usage reported as known cost")
			}
			if scenario == "incomplete" && (last.Cost.Estimated || last.Cost.SpentUSD == 0) {
				t.Fatal("incomplete response usage was lost")
			}
			if err := log.replay(t.Context(), sessionTestServer(t)); err != nil {
				t.Fatal(err)
			}
			// Raising the host limit must preserve spend; uncertain requests stay blocked.
			for _, s := range server.sessions {
				before := s.Cost().SpentUSD
				if err := s.SetCostLimitUSD(1); err != nil {
					t.Fatal(err)
				}
				if s.Cost().SpentUSD != before {
					t.Fatal("reset spend")
				}
				if scenario == "missing usage" {
					if err := s.Provider().RunTurn(t.Context(), TurnInput{Message: "continue"}, s); err == nil {
						t.Fatal("continued with unknown cost")
					}
					if requests != expected {
						t.Fatal("made another paid request")
					}
				}
			}
		})
	}
}

func TestUnknownModelPricing(t *testing.T) {
	p := &openAIProvider{key: "test"}
	if _, err := p.MakeSession(ProviderConfig{Model: "unpriced-model"}); err == nil {
		t.Fatal("unpriced model allowed")
	}
}

func TestCostPersistsAcrossTurns(t *testing.T) {
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTokenCount(w, r) {
			return
		}
		requests++
		fmt.Fprintf(w, `{"status":"completed","usage":%s,"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`, mockUsage)
	}))
	defer api.Close()
	server := sessionTestServer(t)
	server.providers["openai"] = &openAIProvider{key: "test", endpoint: api.URL, client: api.Client()}
	session, err := server.CreateSession("openai", ProviderConfig{Model: "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := session.Provider().RunTurn(t.Context(), TurnInput{Message: "hello"}, session); err != nil {
			t.Fatal(err)
		}
		if math.Abs(session.Cost().SpentUSD-float64(i)*0.0003) > 1e-12 {
			t.Fatalf("turn %d: %+v", i, session.Cost())
		}
	}
	if err := session.SetCostLimitUSD(session.Cost().SpentUSD); err != nil {
		t.Fatal(err)
	}
	if err := session.Provider().RunTurn(t.Context(), TurnInput{Message: "continue"}, session); err == nil || requests != 2 {
		t.Fatalf("third turn bypassed allowance: requests=%d, error=%v", requests, err)
	}
}
