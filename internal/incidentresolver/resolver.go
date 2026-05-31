// Package incidentresolver enriches newly submitted cases with a best-effort
// incident identity before the immutable case_id and output_root are allocated.
package incidentresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

const defaultEtherscanBaseURL = "https://api.etherscan.io/v2/api"

type Resolver struct {
	Enabled          bool
	EtherscanAPIKey  string
	EtherscanBaseURL string
	RPCURL           string
	Client           *http.Client
}

type Result struct {
	Protocol string
	Source   string
	Address  string
}

func (r *Resolver) Configured() bool {
	return r != nil && r.Enabled && strings.TrimSpace(r.EtherscanAPIKey) != ""
}

func (r *Resolver) EnrichIncidentMetadata(ctx context.Context, chain, txHash string, metadata json.RawMessage) (json.RawMessage, error) {
	if store.HasIncidentIdentity(metadata) || !r.Configured() {
		return metadata, nil
	}
	res, err := r.Resolve(ctx, chain, txHash)
	if err != nil || res.Protocol == "" {
		return metadata, nil
	}
	return mergeMetadata(metadata, map[string]any{
		"protocol":         res.Protocol,
		"protocol_source":  res.Source,
		"protocol_address": res.Address,
	}), nil
}

func (r *Resolver) Resolve(ctx context.Context, chain, txHash string) (Result, error) {
	chainID := etherscanChainID(chain)
	if chainID == "" {
		return Result{}, fmt.Errorf("unsupported chain %q", chain)
	}
	tx, txErr := r.ethGetTransactionByHash(ctx, chainID, txHash)
	receipt, receiptErr := r.ethGetTransactionReceipt(ctx, chainID, txHash)
	candidates := candidateAddresses(tx, receipt)
	if len(candidates) == 0 {
		if txErr != nil {
			return Result{}, txErr
		}
		if receiptErr != nil {
			return Result{}, receiptErr
		}
		return Result{}, errors.New("transaction has no candidate contract addresses")
	}
	var lastErr error
	for _, candidate := range candidates {
		protocol, source, err := r.protocolForAddress(ctx, chainID, candidate.Address)
		if err != nil {
			lastErr = err
			continue
		}
		if protocol != "" {
			return Result{Protocol: protocol, Source: source + ":" + candidate.Source, Address: candidate.Address}, nil
		}
	}
	if lastErr != nil {
		return Result{}, lastErr
	}
	return Result{}, errors.New("no usable protocol found in transaction candidates")
}

type etherscanResponse struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

type txResult struct {
	To    string `json:"to"`
	Input string `json:"input"`
}

type receiptResult struct {
	ContractAddress string `json:"contractAddress"`
	Logs            []struct {
		Address string `json:"address"`
	} `json:"logs"`
}

type candidateAddress struct {
	Address string
	Source  string
}

type sourceResult struct {
	ContractName   string `json:"ContractName"`
	Implementation string `json:"Implementation"`
}

func (r *Resolver) ethGetTransactionByHash(ctx context.Context, chainID, txHash string) (txResult, error) {
	var out txResult
	data, err := r.call(ctx, map[string]string{
		"chainid": chainID,
		"module":  "proxy",
		"action":  "eth_getTransactionByHash",
		"txhash":  txHash,
	})
	if err != nil {
		return out, err
	}
	if string(data) == "null" || len(data) == 0 {
		return out, errors.New("transaction not found")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("parse transaction: %w", err)
	}
	return out, nil
}

func (r *Resolver) ethGetTransactionReceipt(ctx context.Context, chainID, txHash string) (receiptResult, error) {
	var out receiptResult
	data, err := r.call(ctx, map[string]string{
		"chainid": chainID,
		"module":  "proxy",
		"action":  "eth_getTransactionReceipt",
		"txhash":  txHash,
	})
	if err != nil {
		return out, err
	}
	if string(data) == "null" || len(data) == 0 {
		return out, errors.New("receipt not found")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("parse receipt: %w", err)
	}
	return out, nil
}

func candidateAddresses(tx txResult, receipt receiptResult) []candidateAddress {
	seen := map[string]bool{}
	out := make([]candidateAddress, 0, 8)
	add := func(address, source string) {
		address = strings.TrimSpace(strings.ToLower(address))
		if !isAddress(address) || seen[address] {
			return
		}
		seen[address] = true
		out = append(out, candidateAddress{Address: address, Source: source})
	}
	for _, address := range calldataAddresses(tx.Input) {
		add(address, "calldata")
	}
	for _, log := range receipt.Logs {
		add(log.Address, "receipt_log")
	}
	add(receipt.ContractAddress, "contract_creation")
	add(tx.To, "tx_to")
	return out
}

func (r *Resolver) protocolForAddress(ctx context.Context, chainID, address string) (string, string, error) {
	protocol, source, sourceErr := r.contractProtocol(ctx, chainID, address, 0)
	if protocol != "" && !isCommonAssetName(protocol) {
		return protocol, source, nil
	}
	if token, err := r.rpcTokenProtocol(ctx, address); err == nil && token != "" && !isCommonAssetName(token) {
		return token, "rpc_erc20_metadata", nil
	}
	if token, err := r.tokenProtocol(ctx, chainID, address); err == nil && token != "" && !isCommonAssetName(token) {
		return token, "etherscan_token", nil
	}
	if protocol != "" {
		return protocol, source, nil
	}
	return "", "", sourceErr
}

func calldataAddresses(input string) []string {
	input = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(input)), "0x")
	if len(input) < 64 {
		return nil
	}
	if len(input)%64 == 8 {
		input = input[8:]
	}
	if len(input)%64 != 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for i := 0; i+64 <= len(input); i += 64 {
		word := input[i : i+64]
		if !strings.HasPrefix(word, "000000000000000000000000") {
			continue
		}
		address := "0x" + word[24:]
		if !isAddress(address) || seen[address] || address == "0x0000000000000000000000000000000000000000" {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out
}

func (r *Resolver) contractProtocol(ctx context.Context, chainID, address string, depth int) (string, string, error) {
	if depth > 1 {
		return "", "", errors.New("implementation lookup depth exceeded")
	}
	data, err := r.call(ctx, map[string]string{
		"chainid": chainID,
		"module":  "contract",
		"action":  "getsourcecode",
		"address": address,
	})
	if err != nil {
		return "", "", err
	}
	var rows []sourceResult
	if err := json.Unmarshal(data, &rows); err != nil {
		return "", "", fmt.Errorf("parse source code: %w", err)
	}
	if len(rows) == 0 {
		return "", "", errors.New("source code result is empty")
	}
	row := rows[0]
	name := cleanContractName(row.ContractName)
	if isGenericContractName(name) && isAddress(row.Implementation) {
		protocol, source, err := r.contractProtocol(ctx, chainID, row.Implementation, depth+1)
		if err == nil && protocol != "" {
			return protocol, source, nil
		}
	}
	if name == "" || isGenericContractName(name) {
		return "", "", errors.New("contract name is generic or empty")
	}
	return name, "etherscan_contract", nil
}

type rpcResponse struct {
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *Resolver) rpcTokenProtocol(ctx context.Context, address string) (string, error) {
	if strings.TrimSpace(r.RPCURL) == "" {
		return "", errors.New("rpc url is not configured")
	}
	symbol, _ := r.ethCallString(ctx, address, "0x95d89b41")
	name, _ := r.ethCallString(ctx, address, "0x06fdde03")
	return firstNonGeneric(symbol, name), nil
}

func (r *Resolver) ethCallString(ctx context.Context, address, data string) (string, error) {
	result, err := r.ethCall(ctx, address, data)
	if err != nil {
		return "", err
	}
	return decodeABIString(result), nil
}

func (r *Resolver) ethCall(ctx context.Context, address, data string) (string, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "eth_call",
		"params": []any{
			map[string]string{"to": address, "data": data},
			"latest",
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(r.RPCURL), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("rpc status %d", resp.StatusCode)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", errors.New(out.Error.Message)
	}
	return out.Result, nil
}

func decodeABIString(raw string) string {
	hex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if hex == "" || hex == "0" {
		return ""
	}
	if len(hex) == 64 {
		return strings.TrimRight(hexToASCII(hex), "\x00")
	}
	if len(hex) >= 128 {
		lengthHex := hex[64:128]
		length := parseHexLength(lengthHex)
		start := 128
		end := start + length*2
		if length > 0 && end <= len(hex) {
			return strings.TrimRight(hexToASCII(hex[start:end]), "\x00")
		}
	}
	return ""
}

func parseHexLength(s string) int {
	n := 0
	for _, r := range s {
		n *= 16
		switch {
		case r >= '0' && r <= '9':
			n += int(r - '0')
		case r >= 'a' && r <= 'f':
			n += int(r-'a') + 10
		default:
			return 0
		}
		if n > 4096 {
			return 0
		}
	}
	return n
}

func hexToASCII(s string) string {
	var b strings.Builder
	for i := 0; i+2 <= len(s); i += 2 {
		hi, ok1 := fromHex(s[i])
		lo, ok2 := fromHex(s[i+1])
		if !ok1 || !ok2 {
			return ""
		}
		b.WriteByte(byte(hi<<4 | lo))
	}
	return strings.TrimSpace(b.String())
}

func fromHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

type tokenInfoResult struct {
	Name   string `json:"tokenName"`
	Symbol string `json:"symbol"`
}

func (r *Resolver) tokenProtocol(ctx context.Context, chainID, address string) (string, error) {
	data, err := r.call(ctx, map[string]string{
		"chainid":         chainID,
		"module":          "token",
		"action":          "tokeninfo",
		"contractaddress": address,
	})
	if err != nil {
		return "", err
	}
	var rows []tokenInfoResult
	if err := json.Unmarshal(data, &rows); err == nil && len(rows) > 0 {
		return firstNonGeneric(rows[0].Symbol, rows[0].Name), nil
	}
	var row tokenInfoResult
	if err := json.Unmarshal(data, &row); err != nil {
		return "", fmt.Errorf("parse token info: %w", err)
	}
	return firstNonGeneric(row.Symbol, row.Name), nil
}

func firstNonGeneric(values ...string) string {
	for _, value := range values {
		value = cleanContractName(value)
		if value != "" && !isGenericContractName(value) && !isCommonAssetName(value) {
			return value
		}
	}
	return ""
}

func (r *Resolver) call(ctx context.Context, params map[string]string) (json.RawMessage, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	base := strings.TrimSpace(r.EtherscanBaseURL)
	if base == "" {
		base = defaultEtherscanBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse etherscan base url: %w", err)
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	q.Set("apikey", strings.TrimSpace(r.EtherscanAPIKey))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("etherscan status %d", resp.StatusCode)
	}
	var out etherscanResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode etherscan response: %w", err)
	}
	if len(out.Result) == 0 {
		return nil, errors.New("etherscan response missing result")
	}
	if out.Status == "0" && strings.EqualFold(out.Message, "NOTOK") {
		return nil, fmt.Errorf("etherscan not ok: %s", string(out.Result))
	}
	return out.Result, nil
}

func mergeMetadata(metadata json.RawMessage, fields map[string]any) json.RawMessage {
	merged := map[string]any{}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &merged)
	}
	for k, v := range fields {
		if v != nil && fmt.Sprint(v) != "" {
			merged[k] = v
		}
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return metadata
	}
	return data
}

func etherscanChainID(chain string) string {
	switch compact(chain) {
	case "1", "eth", "ethereum", "ethereummainnet", "mainnet":
		return "1"
	case "56", "bsc", "bnb", "binance", "binancesmartchain", "bscmainnet":
		return "56"
	case "137", "polygon", "matic", "polygonpos":
		return "137"
	case "42161", "arb", "arbitrum", "arbitrumone":
		return "42161"
	case "10", "op", "optimism":
		return "10"
	case "8453", "base":
		return "8453"
	case "43114", "avax", "avalanche", "avalanchecchain":
		return "43114"
	case "250", "ftm", "fantom":
		return "250"
	default:
		return ""
	}
}

func cleanContractName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return ""
	}
	if idx := strings.LastIndex(raw, ":"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[idx+1:]
	}
	return strings.TrimSpace(raw)
}

func isGenericContractName(name string) bool {
	switch compact(name) {
	case "", "proxy", "transparentupgradeableproxy", "erc1967proxy", "beaconproxy", "adminupgradeabilityproxy", "initializableimmutableadminupgradeabilityproxy", "implementation", "contract", "erc20", "erc721", "erc1155", "bep20token", "token", "pair", "uniswapv2pair", "pancakepair":
		return true
	default:
		return false
	}
}

func isCommonAssetName(name string) bool {
	switch compact(name) {
	case "weth", "wrappedether", "wbnb", "wrappedbnb", "usdt", "tether", "tetherusd", "usdc", "usdcoin", "dai", "busd", "binanceusd":
		return true
	default:
		return false
	}
}

func isAddress(raw string) bool {
	s := strings.TrimSpace(strings.ToLower(raw))
	if !strings.HasPrefix(s, "0x") || len(s) != 42 {
		return false
	}
	for _, r := range s[2:] {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func compact(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
