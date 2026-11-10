package tumblebug

import (
	"fmt"
	"net/url"
)

func infraPath(nsID, infraID string) string {
	return fmt.Sprintf("/ns/%s/infra/%s", url.PathEscape(nsID), url.PathEscape(infraID))
}
