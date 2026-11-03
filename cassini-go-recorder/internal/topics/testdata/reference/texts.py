# Public (UDHR Article 1) and synthetic texts only. No meeting data.
UDHR = {
 "en": "All human beings are born free and equal in dignity and rights. They are endowed with reason and conscience and should act towards one another in a spirit of brotherhood.",
 "it": "Tutti gli esseri umani nascono liberi ed eguali in dignità e diritti. Essi sono dotati di ragione e di coscienza e devono agire gli uni verso gli altri in spirito di fratellanza.",
 "fr": "Tous les êtres humains naissent libres et égaux en dignité et en droits. Ils sont doués de raison et de conscience et doivent agir les uns envers les autres dans un esprit de fraternité.",
 "de": "Alle Menschen sind frei und gleich an Würde und Rechten geboren. Sie sind mit Vernunft und Gewissen begabt und sollen einander im Geist der Brüderlichkeit begegnen.",
 "es": "Todos los seres humanos nacen libres e iguales en dignidad y derechos y, dotados como están de razón y conciencia, deben comportarse fraternalmente los unos con los otros.",
 "pt": "Todos os seres humanos nascem livres e iguais em dignidade e em direitos. Dotados de razão e de consciência, devem agir uns para com os outros em espírito de fraternidade.",
 "nl": "Alle mensen worden vrij en gelijk in waardigheid en rechten geboren. Zij zijn begiftigd met verstand en geweten, en behoren zich jegens elkander in een geest van broederschap te gedragen.",
 "pl": "Wszyscy ludzie rodzą się wolni i równi pod względem swej godności i swych praw. Są oni obdarzeni rozumem i sumieniem i powinni postępować wobec innych w duchu braterstwa.",
 "ru": "Все люди рождаются свободными и равными в своем достоинстве и правах. Они наделены разумом и совестью и должны поступать в отношении друг друга в духе братства.",
 "el": "Όλοι οι άνθρωποι γεννιούνται ελεύθεροι και ίσοι στην αξιοπρέπεια και τα δικαιώματα. Είναι προικισμένοι με λογική και συνείδηση, και οφείλουν να συμπεριφέρονται μεταξύ τους με πνεύμα αδελφοσύνης.",
 "tr": "Bütün insanlar hür, haysiyet ve haklar bakımından eşit doğarlar. Akıl ve vicdana sahiptirler ve birbirlerine karşı kardeşlik zihniyeti ile hareket etmelidirler.",
 "ar": "يولد جميع الناس أحرارًا متساوين في الكرامة والحقوق. وقد وهبوا عقلاً وضميرًا وعليهم أن يعامل بعضهم بعضًا بروح الإخاء.",
 "he": "כל בני האדם נולדו בני חורין ושווים בערכם ובזכויותיהם. כולם חוננו בתבונה ובמצפון, לפיכך חובה עליהם לנהוג איש ברעהו ברוח של אחוה.",
 "hi": "सभी मनुष्यों को गौरव और अधिकारों के मामले में जन्मजात स्वतन्त्रता और समानता प्राप्त है। उन्हें बुद्धि और अन्तरात्मा की देन प्राप्त है और परस्पर उन्हें भाईचारे के भाव से बर्ताव करना चाहिए।",
 "zh": "人人生而自由，在尊严和权利上一律平等。他们赋有理性和良心，并应以兄弟关系的精神相对待。",
 "ja": "すべての人間は、生まれながらにして自由であり、かつ、尊厳と権利とについて平等である。人間は、理性と良心とを授けられており、互いに同胞の精神をもって行動しなければならない。",
 "ko": "모든 인간은 태어날 때부터 자유로우며 그 존엄과 권리에 있어 동등하다. 인간은 천부적으로 이성과 양심을 부여받았으며 서로 형제애의 정신으로 행동하여야 한다.",
 "th": "มนุษย์ทั้งหลายเกิดมามีอิสระและเสมอภาคกันในเกียรติศักดิ์และสิทธิ",
 "vi": "Tất cả mọi người sinh ra đều được tự do và bình đẳng về nhân phẩm và quyền.",
 "sw": "Watu wote wamezaliwa huru, hadhi na haki zao ni sawa.",
}

SYNTHETIC_SPEECH = [
 "Uh so the the release candidate is uh mostly ready, we just need to fix the the upload retry and then I think we can ship it on Monday.",
 "Okay, um, yeah. I I I looked at the dashboard yesterday and the the numbers look fine, but uh the export still times out for big files.",
 "Allora, ehm, la versione nuova è quasi pronta, manca solo da sistemare il il caricamento e poi la rilasciamo lunedì.",
 "Also, äh, wir haben gestern die die Tests laufen lassen und die meisten sind grün, nur der Export hängt noch.",
 "Bon, euh, on a presque fini la migration, il reste juste à vérifier les sauvegardes avant vendredi.",
]

EDGE = [
 "",
 " ",
 "   leading and trailing   ",
 "double  space   triple",
 "tab\there\tand\tthere",
 "new\nline\n\nparagraph",
 "crlf\r\nline",
 " no break spaces ",
 "zero​width​space and⁠joiner",
 "Launch 🚀 done ✅ 👍🏽 👨‍👩‍👧‍👦 🇮🇹 🏳️‍🌈",
 "été vs été, Å vs Å, ñ vs ñ",
 "Zalgo: Z͓͑͒a͔͕l͖g͗o",
 "1234567890 3.14159 1,000,000 2026-10-14 10:30 -42 +7 0x1F 1e-9 ½ ² ⅞ ١٢٣ ४५६",
 "https://example.com/path?q=1&x=y#frag user@example.org",
 "func main() { fmt.Println(\"hi\") } // comment /* block */ x := a[i]+b*2",
 "Wait... what?!?! — “quoted” ‘single’ «guillemets» (paren) [bracket] {brace} …",
 "a" * 300,
 "ha" * 120,
 "I I I I I I uh uh um um yeah yeah",
 "<start_of_turn>user<end_of_turn> <mask> <image_soft_token>",
 "▁already▁separated▁",
 "Rare: 𐀀 𓀀 🧿 ꙮ \U000E0041 � ",
 "Cassini 版本 2.0 — релиз 🎉 مرحبا שלום",
 "ALL CAPS SHOUTING AND MiXeD CaSe wOrDs",
 "ﬁ ﬂ ﬀ ligatures and Ｆｕｌｌｗｉｄｔｈ ｌｅｔｔｅｒｓ",
 "İstanbul ı I i — straße STRASSE — ǅ ǈ",
]

def long_text():
    words = ("the team reviewed the schedule for the next release and agreed that the "
             "documentation, the installer and the migration notes all need another pass before "
             "anyone outside the project sees them").split()
    return " ".join(words[i % len(words)] for i in range(380))

PROBE = [
 "The weekly planning meeting reviewed the release schedule and the open support tickets.",
 "La riunione settimanale ha rivisto il calendario dei rilasci e le richieste di assistenza aperte.",
 "Das wöchentliche Planungstreffen hat den Zeitplan für die Veröffentlichung besprochen.",
 "週次の計画会議では、リリースの日程と未解決のサポート依頼を確認しました。",
]
