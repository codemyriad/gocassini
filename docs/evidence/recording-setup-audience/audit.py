import sqlite3,re,json,sys
from pathlib import Path
# Usage: python3 audit.py /path/to/archive.db > audit.json
c=sqlite3.connect(Path(sys.argv[1]).resolve().as_uri()+'?mode=ro',uri=True)
rows=c.execute('''SELECT t.topic_id,t.created_at,p.username,p.body FROM forum_topics t JOIN forum_posts p ON p.base_url=t.base_url AND p.topic_id=t.topic_id AND p.post_number=1 WHERE t.base_url=? AND t.created_at>=? AND t.created_at<?''',('https://help.nextcloud.com','2025-01-01','2026-09-12')).fetchall()
# Delimit the installation field; never search examples in the template or the rest of a post.
pattern=re.compile(r'installation method\s*(?:\([^)]*\))?\s*:?[\s*_`-]*(.*?)\s*(?:are you using cloud[fF][ilI]are|summary of the issue|nextcloud server logs|configuration)',re.I|re.S)
filled=[]
for tid,date,user,body in rows:
 m=pattern.search(body or '')
 if not m: continue
 v=re.sub(r'\(e\.g\.[^)]*\)', '', m.group(1), flags=re.I).strip(' :*`_-\n')
 if not v or re.fullmatch(r'(replace me|n/?a|unknown|none|[-?]+)',v,re.I) or len(v)>200:continue
 filled.append((tid,date,user,v))
patterns={'AIO / All-in-One':r'\ba[il]o\b|all[ -]?in[ -]?one','Docker / Compose / Portainer':r'docker|compose|portainer','Bare metal / archive / manual':r'bare.?metal|archive|manual','NextcloudPi / NCP':r'nextcloudpi|\bncp\b','NAS names':r'truenas|unraid|synology|qnap','Snap':r'\bsnap\b','Kubernetes / Helm':r'kubernetes|\bk[38]s\b|helm','Managed/shared/provider names':r'managed|shared|hetzner|ionos|bluehost|hosting'}
counts={};samples={}
for label,pat in patterns.items():
 found=[r for r in filled if re.search(pat,r[3],re.I)]
 counts[label]={'topics':len(found),'authors':len(set(r[2] for r in found))};samples[label]=found[-3:]
result={'period':'2025-01-01 through 2026-09-11','opening_posts':len(rows),'authors':len(set(r[2] for r in rows)),'nonempty_bounded_installation_fields':len(filled),'field_authors':len(set(r[2] for r in filled)),'counts_nonexclusive':counts,'examples':samples}
print(json.dumps(result,indent=2))
