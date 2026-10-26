package gui

import (
	"context"
	"net/http"

	"github.com/yetone/magpie/internal/access"
)

// internalGatewayRequest marks a request that is forwarded from an already
// authenticated Web handler to the in-process gateway. The standalone
// distribution requires an issued client key for ordinary callers, but these
// calls are server-side work initiated by the administrator and never leave
// the process. A synthetic identity keeps them independent of a user's
// revocable client key while still attributing usage to the GitHub assistant.
func internalGatewayRequest(r *http.Request) *http.Request {
	return r.WithContext(access.WithIdentity(r.Context(), access.Identity{
		KeyID:   "everyapi-internal-web",
		KeyName: "EveryAPI Web",
	}))
}

func internalGatewayContext(ctx context.Context) context.Context {
	return access.WithIdentity(ctx, access.Identity{
		KeyID:   "everyapi-internal-web",
		KeyName: "EveryAPI Web",
	})
}
