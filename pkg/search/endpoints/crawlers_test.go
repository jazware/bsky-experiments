package endpoints

import "testing"

func TestIsCrawlerUA(t *testing.T) {
	crawlers := []string{
		"",
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)",
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Twitterbot/1.0",
		"TelegramBot (like TwitterBot)",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"WhatsApp/2.19.81 A",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Iframely/1.3.1 (+https://iframely.com/docs/about)",
		"Mastodon/4.2.0 (http.rb/5.1.1; +https://mastodon.social/)",
		"SkypeUriPreview Preview/0.5",
		"curl/8.5.0",
		"Bluesky Cardyb/1.1",
	}
	for _, ua := range crawlers {
		if !isCrawlerUA(ua) {
			t.Errorf("expected crawler UA to be detected: %q", ua)
		}
	}

	humans := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
		"Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0",
	}
	for _, ua := range humans {
		if isCrawlerUA(ua) {
			t.Errorf("expected human UA to not be detected as crawler: %q", ua)
		}
	}
}
