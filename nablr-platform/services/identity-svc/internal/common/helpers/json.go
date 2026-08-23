package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

type JSONDecodeOptions struct {
	DisallowUnknownFields bool
	UseNumber             bool
}

func DecodeJSON(r io.Reader, dst any, opts JSONDecodeOptions) error {
	dec := json.NewDecoder(r)
	if opts.UseNumber {
		dec.UseNumber()
	}
	if opts.DisallowUnknownFields {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid_json: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("invalid_json: multiple JSON values in body")
	}
	return nil
}

func UnmarshalJSON(data []byte, dst any) error {
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("invalid_json: %w", err)
	}
	return nil
}

func UnmarshalJSONStrict(data []byte, dst any) error {
	return DecodeJSON(bytes.NewReader(data), dst, JSONDecodeOptions{
		DisallowUnknownFields: true,
		UseNumber:             true,
	})
}

func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Println("failed to write JSON response:", err)
	}
}

func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}
