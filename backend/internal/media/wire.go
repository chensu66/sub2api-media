package media

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	NewRepository,
	ProvideUsageLogWriter,
	NewRuntime,
	NewHandler,
)

func ProvideUsageLogWriter(repository service.UsageLogRepository) UsageLogWriter {
	return repository
}
