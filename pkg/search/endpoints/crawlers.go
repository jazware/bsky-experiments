package endpoints

import "strings"

// crawlerUASubstrings are case-insensitive User-Agent fragments identifying
// link-preview crawlers and scrapers. "bot" alone covers Discordbot,
// Twitterbot, Telegrambot, Slackbot, Googlebot, redditbot, and friends.
var crawlerUASubstrings = []string{
	"bot",
	"crawler",
	"spider",
	"preview",
	"facebookexternalhit",
	"whatsapp",
	"skypeuripreview",
	"mastodon",
	"pleroma",
	"misskey",
	"akkoma",
	"iframely",
	"embedly",
	"cardyb",
	"vkshare",
	"viber",
	"snapchat",
	"curl",
	"wget",
	"python",
	"go-http-client",
	"node-fetch",
	"axios",
}

// isCrawlerUA reports whether a User-Agent looks like a link-preview
// crawler rather than a human's browser.
func isCrawlerUA(ua string) bool {
	if ua == "" {
		return true
	}
	ua = strings.ToLower(ua)
	for _, s := range crawlerUASubstrings {
		if strings.Contains(ua, s) {
			return true
		}
	}
	return false
}
