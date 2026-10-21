package plugin

import applog "github.com/gtkit/gin-api/internal/pkg/log"

// Logger is the same project event contract used by the forwarding facade.
// It grants no Stop, SDK access or resource ownership to consumers.
type Logger = applog.Logger
