package incidentresolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnrichIncidentMetadataFromEtherscanContractName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("action") {
		case "eth_getTransactionByHash":
			if q.Get("chainid") != "56" {
				t.Fatalf("chainid = %q, want 56", q.Get("chainid"))
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"to":"0x1111111111111111111111111111111111111111"}}`))
		case "eth_getTransactionReceipt":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"logs":[]}}`))
		case "getsourcecode":
			if q.Get("address") != "0x1111111111111111111111111111111111111111" {
				t.Fatalf("address = %q", q.Get("address"))
			}
			_, _ = w.Write([]byte(`{"status":"1","message":"OK","result":[{"ContractName":"FPCVault"}]}`))
		default:
			t.Fatalf("unexpected action %q", q.Get("action"))
		}
	}))
	defer srv.Close()

	r := &Resolver{Enabled: true, EtherscanAPIKey: "key", EtherscanBaseURL: srv.URL, Client: srv.Client()}
	got, err := r.EnrichIncidentMetadata(context.Background(), "bsc", "0x"+strings.Repeat("a", 64), json.RawMessage(`{"submitted_by":"test"}`))
	if err != nil {
		t.Fatalf("EnrichIncidentMetadata: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if out["protocol"] != "FPCVault" {
		t.Fatalf("protocol = %v, want FPCVault; metadata=%s", out["protocol"], got)
	}
	if out["protocol_source"] != "etherscan_contract:tx_to" {
		t.Fatalf("protocol_source = %v", out["protocol_source"])
	}
}

func TestEnrichIncidentMetadataFallsBackWhenUnconfigured(t *testing.T) {
	r := &Resolver{Enabled: true}
	in := json.RawMessage(`{"submitted_by":"test"}`)
	got, err := r.EnrichIncidentMetadata(context.Background(), "ethereum", "0x"+strings.Repeat("b", 64), in)
	if err != nil {
		t.Fatalf("EnrichIncidentMetadata: %v", err)
	}
	if string(got) != string(in) {
		t.Fatalf("metadata changed without API key: %s", got)
	}
}

func TestEnrichIncidentMetadataKeepsExistingProtocol(t *testing.T) {
	r := &Resolver{Enabled: true, EtherscanAPIKey: "key"}
	in := json.RawMessage(`{"protocol":"Manual"}`)
	got, err := r.EnrichIncidentMetadata(context.Background(), "ethereum", "0x"+strings.Repeat("c", 64), in)
	if err != nil {
		t.Fatalf("EnrichIncidentMetadata: %v", err)
	}
	if string(got) != string(in) {
		t.Fatalf("metadata changed despite existing protocol: %s", got)
	}
}

func TestCalldataAddressesExtractsABIAddressWords(t *testing.T) {
	input := "0x1921e20f" +
		"000000000000000000000000b192d4a737430aa61cea4ce9bfb6432f7d42592f" +
		"000000000000000000000000a1e08e10eb09857a8c6f2ef6cca297c1a081ed6b"
	got := calldataAddresses(input)
	want := []string{"0xb192d4a737430aa61cea4ce9bfb6432f7d42592f", "0xa1e08e10eb09857a8c6f2ef6cca297c1a081ed6b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calldataAddresses = %v, want %v", got, want)
	}
}
