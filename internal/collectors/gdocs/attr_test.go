package gdocs

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/driveactivity/v2"

	"github.com/adavydov/user-activity-dashboard/internal/collectors"
	"github.com/adavydov/user-activity-dashboard/internal/config"
	"github.com/adavydov/user-activity-dashboard/internal/models"
)

const myEmail = "me@x.com"

var myPerson = models.Person{
	Key:         "adavydov",
	DisplayName: "Aleksei Davydov",
	GoogleEmail: myEmail,
}

// newCollector собирает коллектор без сетевых клиентов: тестируется только
// логика сопоставления автора.
func newCollector(identity string, p models.Person) *Collector {
	return &Collector{
		cfg:      config.GoogleConfig{Mode: config.GoogleAuthOAuth},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		identity: identity,
		people:   map[string]personRef{},
		enrich:   map[string]*DocEnrichment{},
		person:   newPersonIdentity(p),
	}
}

func act(actors ...*driveactivity.KnownUser) *driveactivity.DriveActivity {
	a := &driveactivity.DriveActivity{}
	for _, ku := range actors {
		a.Actors = append(a.Actors, &driveactivity.Actor{User: &driveactivity.User{KnownUser: ku}})
	}
	return a
}

// Главный регресс: неопознанный автор не считается «нашим».
func TestMatchActor_UnknownIsNotOurs(t *testing.T) {
	c := newCollector(myEmail, myPerson)
	s := newRunStats()
	if _, _, ok := c.matchActor(act(&driveactivity.KnownUser{PersonName: "people/999"}), s); ok {
		t.Fatal("активность с неопознанным автором приписана пользователю")
	}
	if s.unattributed != 1 {
		t.Fatalf("счётчик отброшенных = %d, ожидалась 1", s.unattributed)
	}
	if len(s.authorsSeen) != 1 {
		t.Fatal("отброшенный автор не попал в диагностику")
	}
}

func TestMatchActor_CurrentUserOnlyWhenSameAccount(t *testing.T) {
	ku := &driveactivity.KnownUser{PersonName: "people/1", IsCurrentUser: true}

	t.Run("учётка API — это он сам", func(t *testing.T) {
		c := newCollector(myEmail, myPerson)
		_, kind, ok := c.matchActor(act(ku), newRunStats())
		if !ok || kind != matchAccount {
			t.Fatalf("собственная активность не засчитана: ok=%v kind=%q", ok, kind)
		}
	})
	t.Run("учётка API — другой человек", func(t *testing.T) {
		c := newCollector("someone-else@x.com", myPerson)
		if _, _, ok := c.matchActor(act(ku), newRunStats()); ok {
			t.Fatal("чужая активность приписана по флагу IsCurrentUser")
		}
	})
	t.Run("учётка API неизвестна", func(t *testing.T) {
		c := newCollector("", myPerson)
		if _, _, ok := c.matchActor(act(ku), newRunStats()); ok {
			t.Fatal("активность засчитана без подтверждённой учётки")
		}
	})
}

// Автор ленты активности приходит как people/<id>; карта наполняется из прав
// доступа и ревизий, и сопоставление должно работать и по email, и по имени.
func TestMatchActor_ByPeopleMap(t *testing.T) {
	c := newCollector("service@proj.iam.gserviceaccount.com", myPerson)
	c.rememberPerson("people/7", "ME@X.COM", "")
	c.rememberPerson("people/8", "other@x.com", "Ivan Petrov")
	c.rememberPerson("people/9", "", "aleksei  davydov") // только имя, лишние пробелы

	cases := []struct {
		name     string
		id       string
		wantOK   bool
		wantKind matchKind
	}{
		{"по email", "people/7", true, matchEmail},
		{"чужой", "people/8", false, matchNone},
		{"по имени, когда email не отдали", "people/9", true, matchName},
	}
	for _, tc := range cases {
		_, kind, ok := c.matchActor(act(&driveactivity.KnownUser{PersonName: tc.id}), newRunStats())
		if ok != tc.wantOK || kind != tc.wantKind {
			t.Errorf("%s: ok=%v kind=%q, ожидалось ok=%v kind=%q", tc.name, ok, kind, tc.wantOK, tc.wantKind)
		}
	}
}

func TestMatchUser(t *testing.T) {
	c := newCollector("service@proj.iam.gserviceaccount.com", myPerson)
	c.rememberPerson("people/7", myEmail, "")
	own := newCollector(myEmail, myPerson)

	cases := []struct {
		name string
		c    *Collector
		u    *drive.User
		want matchKind
	}{
		{"email совпал, регистр не важен", c, &drive.User{EmailAddress: "ME@X.com"}, matchEmail},
		{"email чужой", c, &drive.User{EmailAddress: "other@x.com"}, matchNone},
		{"email чужой перевешивает совпадение имени", c,
			&drive.User{EmailAddress: "other@x.com", DisplayName: "Aleksei Davydov"}, matchNone},
		{"только имя — это основной случай комментариев", c,
			&drive.User{DisplayName: "Aleksei Davydov"}, matchName},
		{"имя с «ё» и лишними пробелами", c, &drive.User{DisplayName: "  ALEKSEI   DAVYDOV "}, matchName},
		{"чужое имя", c, &drive.User{DisplayName: "Ivan Petrov"}, matchNone},
		{"Me=true и учётка его", own, &drive.User{Me: true}, matchAccount},
		{"Me=true, но учётка чужая", c, &drive.User{Me: true}, matchNone},
		{"по permissionId из карты", c, &drive.User{PermissionId: "7"}, matchEmail},
		{"permissionId неизвестен", c, &drive.User{PermissionId: "999"}, matchNone},
		{"пустой автор", c, nil, matchNone},
	}
	for _, tc := range cases {
		if got := tc.c.matchUser(tc.u); got != tc.want {
			t.Errorf("%s: matchUser = %q, ожидалось %q", tc.name, got, tc.want)
		}
	}
}

// Человека не по чему опознать — это ошибка конфигурации, а не повод собрать всё подряд.
func TestEmptyIdentity(t *testing.T) {
	c := newCollector(myEmail, models.Person{Key: "nobody"})
	if !c.person.empty() {
		t.Fatal("пустая карточка человека не распознана как пустая")
	}
	if got := c.matchUser(&drive.User{EmailAddress: "anyone@x.com"}); got != matchNone {
		t.Fatalf("без признаков человека засчитан автор: %q", got)
	}
}

func TestCommentEvent(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	req := collectors.Request{Person: myPerson, From: from, To: from.AddDate(0, 0, 30)}
	info := docInfo{ID: "doc1", Name: "TSD", URL: "https://docs.google.com/document/d/doc1/edit", IssueKey: "TSD-1"}
	inRange := from.AddDate(0, 0, 2).Format(time.RFC3339)
	c := newCollector(myEmail, myPerson)

	t.Run("чужой комментарий отсеян и попал в диагностику", func(t *testing.T) {
		s := newRunStats()
		_, ok := c.commentEvent(req, info, "c1", "",
			&drive.User{DisplayName: "Ivan Petrov", EmailAddress: "ivan@x.com"}, inRange, "текст", s)
		if ok {
			t.Fatal("чужой комментарий попал в события")
		}
		if s.foreignComments != 1 {
			t.Fatalf("счётчик чужих комментариев = %d, ожидался 1", s.foreignComments)
		}
		if _, seen := s.authorsSeen["Ivan Petrov <ivan@x.com>"]; !seen {
			t.Fatalf("автор не попал в диагностику: %v", s.authorsSeen)
		}
	})

	t.Run("комментарий без email опознан по имени", func(t *testing.T) {
		ev, ok := c.commentEvent(req, info, "c2", "",
			&drive.User{DisplayName: "Aleksei Davydov"}, inRange, "текст", newRunStats())
		if !ok {
			t.Fatal("собственный комментарий без email не засчитан")
		}
		if ev.Meta["matched_by"] != string(matchName) {
			t.Fatalf("признак сопоставления не записан: %v", ev.Meta["matched_by"])
		}
	})

	t.Run("свой комментарий по email", func(t *testing.T) {
		ev, ok := c.commentEvent(req, info, "c1", "",
			&drive.User{EmailAddress: myEmail}, inRange, "текст", newRunStats())
		if !ok {
			t.Fatal("собственный комментарий не засчитан")
		}
		if ev.Type != models.TypeDocComment || ev.RefID != "doc1" || ev.ParentRefID != "TSD-1" {
			t.Fatalf("некорректное событие: %+v", ev)
		}
		if ev.ExternalID != "comment:doc1:c1:" {
			t.Fatalf("ExternalID = %q", ev.ExternalID)
		}
	})

	t.Run("ответ в треде отличается от комментария", func(t *testing.T) {
		a, _ := c.commentEvent(req, info, "c1", "", &drive.User{EmailAddress: myEmail}, inRange, "т", newRunStats())
		b, _ := c.commentEvent(req, info, "c1", "r1", &drive.User{EmailAddress: myEmail}, inRange, "т", newRunStats())
		if a.ID == b.ID {
			t.Fatal("комментарий и ответ получили одинаковый id — один затрёт другой")
		}
	})

	t.Run("вне периода отброшен", func(t *testing.T) {
		old := from.AddDate(0, 0, -5).Format(time.RFC3339)
		if _, ok := c.commentEvent(req, info, "c9", "", &drive.User{EmailAddress: myEmail}, old, "т", newRunStats()); ok {
			t.Fatal("комментарий вне периода засчитан")
		}
	})
}

// Примечание к прогону должно показывать, чьи действия отброшены, — без этого
// расследовать «почему статистика не та» невозможно.
func TestNoteShowsAuthors(t *testing.T) {
	s := newRunStats()
	s.setMode("oauth", "me@x.com")
	s.setPerson(myEmail)
	s.seeAuthor("Ivan Petrov <ivan@x.com>")
	s.seeAuthor("Ivan Petrov <ivan@x.com>")
	s.seeAuthor("Maria S <maria@x.com>")
	s.countMatch(matchName)
	s.incUnattributed()

	n := s.note()
	for _, want := range []string{
		"ищем активность " + myEmail,
		"Ivan Petrov <ivan@x.com> — 2",
		"Maria S <maria@x.com> — 1",
		"автор опознан по: display_name=1",
	} {
		if !strings.Contains(n, want) {
			t.Errorf("в примечании нет %q:\n%s", want, n)
		}
	}
}
