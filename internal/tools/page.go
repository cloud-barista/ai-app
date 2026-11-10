package tools

import (
	"encoding/json"
	"fmt"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// withPage adds the limit and offset every list tool takes to its input
// properties.
func withPage(properties map[string]any) map[string]any {
	properties["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": model.MaxPageLimit,
		"default": model.DefaultPageLimit, "description": fmt.Sprintf("Most items to return, from 1 to %d; %d "+
			"when omitted. The result gives total, the count of every item the filters keep, so a caller can ask "+
			"for the next page with offset", model.MaxPageLimit, model.DefaultPageLimit)}
	properties["offset"] = map[string]any{"type": "integer", "minimum": 0, "default": 0,
		"description": "Items to skip in the order of the list, after the filters; 0 when omitted. Past the end " +
			"gives an empty list"}
	return properties
}

// pageArgs reads the limit and offset of a list tool; see model.NewPage.
func pageArgs(input json.RawMessage) (model.Page, error) {
	var args struct {
		Limit  *int `json:"limit"`
		Offset *int `json:"offset"`
	}
	if len(input) > 0 {
		if err := decode(input, &args); err != nil {
			return model.Page{}, err
		}
	}
	return model.NewPage(args.Limit, args.Offset)
}
