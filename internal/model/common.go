// Package model holds request and response types shared across layers.
package model

// SimpleMsg is the generic message body returned for errors and simple results.
type SimpleMsg struct {
	Message string `json:"message" example:"AI application not found"`
}
