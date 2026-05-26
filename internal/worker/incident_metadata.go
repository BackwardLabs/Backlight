package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type rcaReportForMetadata struct {
	Vulnerability struct {
		AffectedContracts []struct {
			Address string `json:"address"`
			Name    string `json:"name"`
			Role    string `json:"role"`
		} `json:"affected_contracts"`
		Title string `json:"title"`
	} `json:"vulnerability"`
	Protocol string `json:"protocol"`
	Project  string `json:"project"`
}

type addressDBForMetadata struct {
	Addresses []struct {
		Address       string `json:"address"`
		Label         string `json:"label"`
		Name          string `json:"name"`
		ContractName  string `json:"contract_name"`
		TokenMetadata *struct {
			Symbol string `json:"symbol"`
			Name   string `json:"name"`
		} `json:"token_metadata"`
	} `json:"addresses"`
}

func inferIncidentMetadata(outputRoot string) map[string]any {
	report := readRCAReportForMetadata(outputRoot)
	if report == nil {
		return nil
	}
	if protocol := firstNonGeneric(report.Protocol, report.Project); protocol != "" {
		return map[string]any{"protocol": protocol, "protocol_source": "rca_report"}
	}

	victimAddress, victimName := primaryAffectedContract(*report)
	addressDB := readAddressDBForMetadata(outputRoot)
	if addressDB != nil && victimAddress != "" {
		if label := labelForAddress(*addressDB, victimAddress); label != "" {
			return map[string]any{
				"protocol":                 label,
				"protocol_source":          "rca_address_db",
				"vulnerable_contract":      victimAddress,
				"vulnerable_contract_name": victimName,
			}
		}
	}
	if label := firstNonGeneric(victimName); label != "" {
		return map[string]any{
			"protocol":                 label,
			"protocol_source":          "rca_report_affected_contract",
			"vulnerable_contract":      victimAddress,
			"vulnerable_contract_name": victimName,
		}
	}
	return nil
}

func readRCAReportForMetadata(outputRoot string) *rcaReportForMetadata {
	path := filepath.Join(outputRoot, "artifacts", "rca", "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var report rcaReportForMetadata
	if err := json.Unmarshal(data, &report); err != nil {
		return nil
	}
	return &report
}

func readAddressDBForMetadata(outputRoot string) *addressDBForMetadata {
	for _, rel := range []string{
		filepath.Join("artifacts", "rca", "input", "address_db.json"),
		filepath.Join("artifacts", "enrich", "address_labels.json"),
	} {
		data, err := os.ReadFile(filepath.Join(outputRoot, rel))
		if err != nil {
			continue
		}
		var db addressDBForMetadata
		if err := json.Unmarshal(data, &db); err == nil && len(db.Addresses) > 0 {
			return &db
		}
	}
	return nil
}

func primaryAffectedContract(report rcaReportForMetadata) (string, string) {
	for _, contract := range report.Vulnerability.AffectedContracts {
		role := strings.ToLower(contract.Role)
		if strings.Contains(role, "primary") || strings.Contains(role, "vulnerable") {
			return strings.ToLower(strings.TrimSpace(contract.Address)), strings.TrimSpace(contract.Name)
		}
	}
	if len(report.Vulnerability.AffectedContracts) > 0 {
		contract := report.Vulnerability.AffectedContracts[0]
		return strings.ToLower(strings.TrimSpace(contract.Address)), strings.TrimSpace(contract.Name)
	}
	return "", ""
}

func labelForAddress(db addressDBForMetadata, address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, entry := range db.Addresses {
		if strings.ToLower(strings.TrimSpace(entry.Address)) != address {
			continue
		}
		if entry.TokenMetadata != nil {
			if label := firstNonGeneric(entry.TokenMetadata.Symbol, entry.TokenMetadata.Name); label != "" {
				return label
			}
		}
		return firstNonGeneric(entry.Name, entry.ContractName, entry.Label)
	}
	return ""
}

func firstNonGeneric(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || isGenericProtocolLabel(trimmed) {
			continue
		}
		return trimmed
	}
	return ""
}

func isGenericProtocolLabel(value string) bool {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.NewReplacer("_", "", "-", "", " ", "").Replace(key)
	switch key {
	case "token", "contract", "pair", "pancakepair", "pancakerouter", "erc1967proxy", "observedattackcontract", "bep20tokenimplementation":
		return true
	default:
		return strings.HasPrefix(key, "0x")
	}
}
