package gdocs

import (
	"sort"
	"strings"

	"google.golang.org/api/drive/v3"

	"github.com/adavydov/user-activity-dashboard/internal/models"
)

// personRef — то, что удалось узнать о человеке из Drive: адрес и отображаемое
// имя. Drive Activity отдаёт актора как "people/<id>", и без такой карты
// сопоставить его с конкретным сотрудником не с чем.
type personRef struct {
	Email string // в нижнем регистре
	Name  string // нормализованное отображаемое имя
}

// personIdentity — все признаки, по которым мы узнаём нужного человека.
//
// Одного email недостаточно на практике: Drive Comments API сплошь и рядом
// отдаёт автора комментария без адреса — только displayName. Поэтому имя
// участвует в сопоставлении наравне с почтой, но события, опознанные по имени,
// помечаются в meta: имя менее надёжный признак, чем адрес.
type personIdentity struct {
	emails map[string]bool
	names  map[string]bool
	// primary — основной email, для сообщений и подсказок.
	primary string
}

// newPersonIdentity собирает признаки из карточки человека.
func newPersonIdentity(p models.Person) personIdentity {
	id := personIdentity{
		emails: map[string]bool{},
		names:  map[string]bool{},
	}
	for _, e := range []string{p.GoogleEmail, p.Email} {
		if v := normEmail(e); v != "" {
			if id.primary == "" {
				id.primary = v
			}
			id.emails[v] = true
			// Локальная часть адреса часто совпадает с логином в имени
			// аккаунта, но самостоятельным признаком не является — только
			// как имя, если оно вдруг придёт именно в таком виде.
		}
	}
	if v := normName(p.DisplayName); v != "" {
		id.names[v] = true
	}
	return id
}

// empty сообщает, что человека не по чему опознать.
func (id personIdentity) empty() bool { return len(id.emails) == 0 && len(id.names) == 0 }

// primaryEmail — основной адрес человека (может быть пустым).
func (id personIdentity) primaryEmail() string { return id.primary }

// hasEmail сообщает, что адрес принадлежит нужному человеку.
func (id personIdentity) hasEmail(email string) bool {
	v := normEmail(email)
	return v != "" && id.emails[v]
}

// hasName сообщает, что отображаемое имя принадлежит нужному человеку.
func (id personIdentity) hasName(name string) bool {
	v := normName(name)
	return v != "" && id.names[v]
}

// matchKind — по какому признаку опознан автор. Уезжает в meta события,
// чтобы в UI было видно, насколько сопоставление надёжно.
type matchKind string

const (
	matchNone    matchKind = ""
	matchEmail   matchKind = "email"
	matchName    matchKind = "display_name"
	matchAccount matchKind = "api_account"
)

// matchUser сопоставляет пользователя Drive (автора комментария, последнего
// редактора файла) с нужным человеком.
func (c *Collector) matchUser(u *drive.User) matchKind {
	if u == nil || c.person.empty() {
		return matchNone
	}
	// Адрес — сильнейший признак, причём в обе стороны: если он есть и не наш,
	// это другой человек, и совпадение имени ничего не меняет. Тёзки встречаются
	// чаще, чем хотелось бы.
	if email := normEmail(u.EmailAddress); email != "" {
		if c.person.hasEmail(email) {
			return matchEmail
		}
		return matchNone
	}
	if c.person.hasName(u.DisplayName) {
		return matchName
	}
	// Флаг me означает «это владелец токена». Засчитываем, только если токен
	// принадлежит тому самому человеку, иначе на его дашборд уедет чужое.
	if u.Me && c.authenticatedAsPerson() {
		return matchAccount
	}
	// Последний шанс — карта people-id, собранная из прав доступа и ревизий.
	if u.PermissionId != "" {
		if ref, ok := c.lookupPerson("people/" + u.PermissionId); ok {
			return c.matchRef(ref)
		}
	}
	return matchNone
}

// matchRef сопоставляет запись из карты people-id с нужным человеком.
func (c *Collector) matchRef(ref personRef) matchKind {
	if ref.Email != "" {
		if c.person.hasEmail(ref.Email) {
			return matchEmail
		}
		return matchNone
	}
	if c.person.hasName(ref.Name) {
		return matchName
	}
	return matchNone
}

// authenticatedAsPerson сообщает, совпадает ли учётка, которой мы ходим в
// Google API, с человеком, чью активность собираем.
func (c *Collector) authenticatedAsPerson() bool {
	return c.identity != "" && c.person.hasEmail(c.identity)
}

// rememberPerson кладёт в карту то, что узнали о человеке. Пустые поля не
// затирают уже известные.
func (c *Collector) rememberPerson(personName, email, displayName string) {
	if personName == "" || personName == "people/" {
		return
	}
	email = normEmail(email)
	name := normName(displayName)
	if email == "" && name == "" {
		return
	}

	c.peopleMu.Lock()
	defer c.peopleMu.Unlock()
	ref := c.people[personName]
	if email != "" {
		ref.Email = email
	}
	if name != "" {
		ref.Name = name
	}
	c.people[personName] = ref
}

// rememberUser пополняет карту из пользователя Drive.
// PermissionId совпадает с id в "people/<id>" ленты активности.
func (c *Collector) rememberUser(u *drive.User) {
	if u == nil {
		return
	}
	c.rememberPerson("people/"+u.PermissionId, u.EmailAddress, u.DisplayName)
}

// lookupPerson отдаёт то, что известно про people-id.
func (c *Collector) lookupPerson(personName string) (personRef, bool) {
	if personName == "" {
		return personRef{}, false
	}
	c.peopleMu.RLock()
	defer c.peopleMu.RUnlock()
	ref, ok := c.people[personName]
	return ref, ok
}

// describePerson возвращает читаемое описание people-id для диагностики.
func (c *Collector) describePerson(personName string) string {
	ref, ok := c.lookupPerson(personName)
	if !ok {
		return personName
	}
	switch {
	case ref.Name != "" && ref.Email != "":
		return ref.Name + " <" + ref.Email + ">"
	case ref.Email != "":
		return ref.Email
	case ref.Name != "":
		return ref.Name
	default:
		return personName
	}
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// normName приводит имя к сравнимому виду: нижний регистр, схлопнутые пробелы,
// «ё» → «е». Порядок слов не трогаем — «Иван Петров» и «Петров Иван» это
// разные записи, и угадывать здесь опаснее, чем пропустить.
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	return strings.Join(strings.Fields(s), " ")
}

// sortedByCount отдаёт ключи карты по убыванию значения — для диагностики.
func sortedByCount(m map[string]int, limit int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

// describeUser — читаемое описание пользователя Drive для диагностики.
func describeUser(u *drive.User) string {
	if u == nil {
		return "(автор не указан)"
	}
	name := strings.TrimSpace(u.DisplayName)
	email := normEmail(u.EmailAddress)
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case email != "":
		return email
	case name != "":
		return name
	case u.PermissionId != "":
		return "people/" + u.PermissionId
	default:
		return "(автор не указан)"
	}
}
