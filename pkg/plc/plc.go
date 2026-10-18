package plc

import (
	"time"

	"github.com/goccy/go-json"
)

type PLCOperation struct {
	DID       string          `json:"did"`
	CID       string          `json:"cid"`
	CreatedAt time.Time       `json:"createdAt"`
	Nullified bool            `json:"nullified"`
	Operation json.RawMessage `json:"operation"`
}

type PLCOperationPayload struct {
	Type string `json:"type"` // "create", "plc_operation", "plc_tombstone"

	// For plc_operation and create types
	AlsoKnownAs     []string          `json:"alsoKnownAs"`     // Handles in at:// format
	Services        map[string]Service `json:"services"`        // Service endpoints
	VerificationMethods map[string]string `json:"verificationMethods"`
	RotationKeys    []string          `json:"rotationKeys"`

	// Legacy format fields
	Handle          string `json:"handle"`          // Legacy: bare handle
	Service         string `json:"service"`         // Legacy: PDS URL
	RecoveryKey     string `json:"recoveryKey"`
	SigningKey      string `json:"signingKey"`

	Prev string `json:"prev"` // Previous operation CID (null for genesis)
	Sig  string `json:"sig"`  // Signature
}

type Service struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
}

type PLCBatch struct {
	DID           string
	OperationType string
	CreatedAt     time.Time
	Handle        string
	PDS           string
	OperationJSON string
	TimeUS        int64
}

func ExtractHandleAndPDS(op *PLCOperation) (handle string, pds string) {
	var payload PLCOperationPayload
	if err := json.Unmarshal(op.Operation, &payload); err != nil {
		return "", ""
	}

	if len(payload.AlsoKnownAs) > 0 {
		handle = extractHandleFromAKA(payload.AlsoKnownAs[0])
	} else if payload.Handle != "" {
		handle = payload.Handle
	}

	if service, ok := payload.Services["atproto_pds"]; ok {
		pds = service.Endpoint
	} else if payload.Service != "" {
		pds = payload.Service
	}

	return handle, pds
}

func extractHandleFromAKA(aka string) string {
	const prefix = "at://"
	if len(aka) > len(prefix) && aka[:len(prefix)] == prefix {
		return aka[len(prefix):]
	}
	return aka
}

func GetOperationType(op *PLCOperation) string {
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(op.Operation, &payload); err != nil {
		return "unknown"
	}
	if payload.Type == "" {
		return "unknown"
	}
	return payload.Type
}
