import type { FormattersInitializer } from 'typesafe-i18n'
import type { Locales, Formatters } from './i18n-types.js'

import { formatCompactCount, formatCount } from './format.js'

// Formatters a dictionary string names after a `|`: "{count:number|num}".
//
// They write a number in the reader's *locale* (format.ts's formatLocale()),
// which Nextcloud keeps apart from their language, so they ignore the
// `locale` typesafe-i18n hands in. A plural ({{meeting|meetings}}) still picks
// its form from the raw number and the language's plural rules.
export const initFormatters: FormattersInitializer<Locales, Formatters> = () => {
	const formatters: Formatters = {
		// 1,234 in English, 1.234 in German.
		num: (value) => formatCount(value),
		// 7.5K in English: a large count shortened.
		compact: (value) => formatCompactCount(value),
	}

	return formatters
}
