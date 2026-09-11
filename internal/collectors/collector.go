// Package collectors описывает общий контракт сбора активности из внешних систем.
package collectors

import (
	"context"
	"time"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// Request — что и за какой период собирать.
type Request struct {
	Person models.Person
	From   time.Time
	To     time.Time
}

// Result — что собрал коллектор.
type Result struct {
	Events []models.Event
	// DocLinks заполняет только Jira-коллектор: ссылки на Google-документы,
	// найденные в задачах (TSD).
	DocLinks []models.DocLink
	// Days заполняет коллектор календаря: особые дни человека — праздники
	// (по офису из HRDB) и отпуска. Оркестратор пишет их в person_days.
	Days []models.PersonDay
	// Note — необязательное пояснение для UI (например, «search API недоступен,
	// собрано через conversations.history»).
	Note string
	// ResolvedAccount — аккаунт человека в источнике, найденный по email во
	// время сбора (например, Slack user id). Оркестратор сохраняет его в
	// карточку, чтобы не искать заново на каждом прогоне.
	ResolvedAccount string
}

// Collector — источник активности.
type Collector interface {
	// Source возвращает идентификатор источника.
	Source() models.Source
	// Collect выгружает активность за период. Реализация обязана уважать
	// отмену контекста и не писать в БД — это делает оркестратор.
	Collect(ctx context.Context, req Request) (Result, error)
}

// Progress — колбэк прогресса, опционально поддерживаемый коллекторами.
type Progress func(stage string, done, total int)

// ProgressAware — коллектор, умеющий сообщать о прогрессе.
type ProgressAware interface {
	SetProgress(Progress)
}
