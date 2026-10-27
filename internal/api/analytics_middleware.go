package api

import (
	"context"
	"strings"

	"denova/internal/analytics"
	application "denova/internal/app"
	"github.com/cloudwego/hertz/pkg/app"
)

// Only authenticated, registered API routes are measured. A route is reduced
// locally to a fixed feature; paths, IDs, bodies and diagnostics never leave
// the host. Analytics endpoints are excluded to prevent feedback loops.
func analyticsMiddleware(application *application.App) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if recovered := recover(); recovered != nil {
				application.Analytics().RecoveredPanic()
				panic(recovered) // Preserve Hertz's existing recovery and response.
			}
		}()
		c.Next(ctx)
		route := c.FullPath()
		if route == "" || !strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/api/analytics") {
			return
		}
		if status := c.Response.StatusCode(); status >= 400 {
			application.Analytics().APIError(analyticsRouteFeature(route), status)
		}
	}
}

func analyticsRouteFeature(route string) analytics.Feature {
	switch {
	case strings.Contains(route, "/interactive"):
		return analytics.Game
	case strings.Contains(route, "/agent-chat"):
		return analytics.Chat
	case strings.Contains(route, "/images"):
		return analytics.Image
	case strings.Contains(route, "/lore"):
		return analytics.Library
	case strings.Contains(route, "/versions"):
		return analytics.Versions
	case strings.Contains(route, "/automation"):
		return analytics.Automation
	case strings.Contains(route, "/market"), strings.Contains(route, "/platform"):
		return analytics.Resources
	case strings.Contains(route, "/book"), strings.Contains(route, "/chat"):
		return analytics.Writing
	case strings.Contains(route, "/projects"):
		return analytics.Projects
	}
	return ""
}
