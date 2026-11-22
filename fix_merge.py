import re, pathlib

# --- api/openapi.yaml ---
p = pathlib.Path('/tmp/review2026/cloudlet/api/openapi.yaml')
s = p.read_text()
block = re.search(r'<<<<<<< HEAD\n(.*?)=======\n(.*?)>>>>>>> origin/master\n', s, re.S)
head, master = block.group(1), block.group(2)
# keep Session first then Operation, matching alphabetical-ish order used by generator
s = s[:block.start()] + master + head + s[block.end():]
p.write_text(s)

# --- web/src/api.gen.ts ---
p = pathlib.Path('/tmp/review2026/cloudlet/web/src/api.gen.ts')
s = p.read_text()
block = re.search(r'<<<<<<< HEAD\n(.*?)=======\n(.*?)>>>>>>> origin/master\n', s, re.S)
head, master = block.group(1), block.group(2)
# head ends with "};" of Operation; master ends with "projects: ...[]" without closing brace
# Compose: Operation block (strip trailing "};") + Session block + "        };\n"
head_lines = head.rstrip('\n')
assert head_lines.endswith('};'), head_lines[-20:]
head_body = head_lines[:-2].rstrip('\n')
replacement = head_body + '\n' + master.rstrip('\n') + '\n        };\n'
s = s[:block.start()] + replacement + s[block.end():]
p.write_text(s)
print("done")
