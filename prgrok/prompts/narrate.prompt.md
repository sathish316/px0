You are producing a short **spoken script** ("talking points") for a
Twitch-style live code review of a pull request or commit (see `kind` in the input). Below are the already-written
**{{source}}** sections. Rewrite EACH into a brief, natural, first-person spoken
talking point, like a streamer walking a viewer through the change. You are
answering in ONE shot. Do not read or modify any file except the output file named below.

## Hard constraints

- The WHOLE script must be speakable in about `minutes` minute(s): roughly
  `budget_words` words TOTAL across all sections. Stay under it.
- Each talking point must be briefer than its source `text`. Keep the essential
  what and why, cut filler. Short sentences. Plain spoken prose only: no
  Markdown, no code fences, no headings, no emoji, no file or line minutiae.
- Keep the overview slightly fuller, every other point tight (1 to 3 sentences).
- Use the section `id` values exactly. Do not invent, rename, or drop ids.

## Input

```json
{{input_json}}
```

## Output

Write a single JSON object to this file (create its directory if missing):

`{{output_path}}`

The file must contain that JSON object and nothing else: no prose, no markdown
fence. It must satisfy this JSON Schema:

```json
{{output_schema}}
```

## Example output

Example of the shape and tone (different PR; use the section ids from the input and rewrite their text):

```json
{{output_example}}
```

## JSON writing rules

- Never put a double quote `"` inside a string value; use single quotes or
  backticks for code and names. This is the most common way the file ends up
  unparseable.
- Write a line break inside a string as the two characters `\n`, never as a real
  line break. Escape any backslash as `\\`.
- If you can, build the object in a script (for example Python's `json.dump`)
  and write the file with it, so the escaping is correct, then check that it
  parses.

## When done

After writing the file, reply with the single word `done`. Do NOT print the JSON
in your reply. Only if you are unable to write the file, print the JSON object
instead.
