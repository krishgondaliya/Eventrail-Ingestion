package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const MaxJSONRequestBodyBytes int64 = 1 << 20

var ErrExtraJSONValue = errors.New("request body must contain exactly one JSON value")

func DecodeJSONRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONRequest(w, r, dst, MaxJSONRequestBodyBytes)
}

func IsRequestBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func decodeJSONRequest(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrExtraJSONValue
		}
		return err
	}

	return nil
}
