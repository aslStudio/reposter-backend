package rewrite

import "testing"

func TestStripChannelSignatures(t *testing.T) {
	tests := []struct{ input, want string }{
		{"Новость о запуске.\n\nПодписывайтесь: @other_channel", "Новость о запуске."},
		{"Важная новость.\n\nНаш канал\nhttps://t.me/other_channel", "Важная новость."},
		{"Текст новости.\n\n[Канал для заметок](https://t.me/other_channel)", "Текст новости."},
		{"В посте процитировали @other_channel.\n\nВывод автора.", "В посте процитировали @other_channel.\n\nВывод автора."},
		{"По словам @other_channel, данные обновят завтра.", "По словам @other_channel, данные обновят завтра."},
		{"**Заголовок**\n\nСодержание.\n\n@other_channel", "**Заголовок**\n\nСодержание."},
	}
	for _, tt := range tests {
		if got := StripChannelSignatures(tt.input); got != tt.want {
			t.Errorf("StripChannelSignatures(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}
