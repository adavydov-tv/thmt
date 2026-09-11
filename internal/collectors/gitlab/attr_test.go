package gitlab

import "testing"

// Коммит принадлежит человеку только по author_email: совпадение committer
// (rebase/cherry-pick чужой ветки) и голого имени при известном email — не матч.
func TestMatchesAuthor(t *testing.T) {
	cases := []struct {
		name  string
		cm    glCommit
		email string
		disp  string
		want  bool
	}{
		{"author_email совпал", glCommit{AuthorEmail: "Me@x.tv"}, "me@x.tv", "Me", true},
		{"committer_email не засчитывается", glCommit{AuthorEmail: "other@x.tv", CommitterEmail: "me@x.tv"}, "me@x.tv", "Me", false},
		{"имя при известном email не засчитывается", glCommit{AuthorEmail: "personal@gmail.com", AuthorName: "Me"}, "me@x.tv", "Me", false},
		{"имя без email — деградационный матч", glCommit{AuthorName: "Me"}, "", "Me", true},
		{"чужое имя без email", glCommit{AuthorName: "Other"}, "", "Me", false},
	}
	for _, c := range cases {
		if got := matchesAuthor(&c.cm, c.email, c.disp); got != c.want {
			t.Errorf("%s: matchesAuthor = %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

// Мерж/закрытие MR из пути author_id засчитывается только выполнившему.
func TestActedBy(t *testing.T) {
	me, other := &glUser{ID: 7}, &glUser{ID: 9}
	if !actedBy(me, nil, 7) {
		t.Error("свой мерж (merge_user) не засчитан")
	}
	if !actedBy(nil, me, 7) {
		t.Error("свой мерж (устаревший merged_by) не засчитан")
	}
	if actedBy(other, nil, 7) {
		t.Error("чужой мерж засчитан автору MR")
	}
	if actedBy(nil, nil, 7) {
		t.Error("мерж без указания исполнителя (старый GitLab) засчитан — должен быть fail-closed")
	}
}
