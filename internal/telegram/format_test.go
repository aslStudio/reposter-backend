package telegram

import "testing"

func TestRenderHTML(t *testing.T) {
	tests := []struct{ input, want string }{
		{"**Канал для заметок**\n\nЗдесь будут мысли.", "<b>Канал для заметок</b>\n\nЗдесь будут мысли."},
		{"### Канал для заметок\n\nАвтор планирует писать.", "<b>Канал для заметок</b>\n\nАвтор планирует писать."},
		{"### **Новости & <тест>**\n\n**Важно**: 2 < 3", "<b>Новости &amp; &lt;тест&gt;</b>\n\n<b>Важно</b>: 2 &lt; 3"},
		{"Это **непарная разметка", "Это **непарная разметка"},
		{"*курсив* __подчёркнуто__ ~~зачёркнуто~~ `код` ||спойлер||", "<i>курсив</i> <u>подчёркнуто</u> <s>зачёркнуто</s> <code>код</code> <tg-spoiler>спойлер</tg-spoiler>"},
		{"> Цитата & текст", "<blockquote>Цитата &amp; текст</blockquote>"},
		{"[Сайт](https://example.com/?a=1&b=2) [опасно](javascript:alert)", "<a href=\"https://example.com/?a=1&amp;b=2\">Сайт</a> [опасно](javascript:alert)"},
	}
	for _, tt := range tests {
		if got := renderHTML(tt.input); got != tt.want {
			t.Errorf("renderHTML(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

func TestRenderPublicationSignature(t *testing.T) {
	got, visible, err := renderPublication("### Новости <&>", "@My_Channel", PublishOptions{FormattingEnabled: true, SignatureText: "Читать @My_Channel"})
	if err != nil {
		t.Fatal(err)
	}
	want := "<b>Новости &lt;&amp;&gt;</b>\n\n<a href=\"https://t.me/my_channel\">Читать @My_Channel</a>"
	if got != want || visible != len([]rune("### Новости <&>\n\nЧитать @My_Channel")) {
		t.Fatalf("got %q, visible %d", got, visible)
	}

	plain, _, err := renderPublication("**Без разметки**", "@my_channel", PublishOptions{SignatureText: "Мы"})
	if err != nil || plain != "**Без разметки**\n\n<a href=\"https://t.me/my_channel\">Мы</a>" {
		t.Fatalf("plain publication: %q, %v", plain, err)
	}
	if _, _, err := renderPublication("post", "-1001234567890", PublishOptions{SignatureText: "Мы"}); err == nil {
		t.Fatal("accepted signature without a public channel username")
	}
}
