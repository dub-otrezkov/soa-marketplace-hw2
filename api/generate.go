// Package contract owns the canonical OpenAPI source and reproducible code generation.
package contract

import _ "embed"

// OpenAPI is the same canonical contract used by the generator and request validator.
//
//go:embed openapi.yaml
var OpenAPI []byte

//go:generate go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml
