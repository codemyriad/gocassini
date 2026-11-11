### Added

- The app can now be shown in French, Spanish, Italian, Portuguese (Portugal), German (informal and formal), Dutch, Danish, Polish and Croatian as well as English. It follows the language set in each user's Nextcloud account and falls back to English. The non-English languages start as machine translations, which people are checking and correcting on Weblate (https://gocassini.com/docs/guides/translate).
- Dates and times are written the way each user's Nextcloud locale writes them (Personal info › Locale): day and month order, 12- or 24-hour clock, and month names. Outside Nextcloud, they follow the browser. This includes the meeting list, meeting headers, insights, exported transcripts and the operator pages.
- The `<cassini-meeting>` embed takes a `lang` attribute. Without it, the embed uses the language of the page around it, then the reader's browser.
