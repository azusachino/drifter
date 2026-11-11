Task: Foreign Original Work Resolution
Given localized edition metadata (title, author, translator, publisher, ISBN, catalog original title), determine the original (foreign) title and author to assist cross-language catalog matching on Hardcover.

Abstention Rules:
- If the work is written natively in the local language (e.g. domestic technical manual, domestic novel) with no translator and no foreign original, set abstained to true and confidence to 0.0. Leave original_title and original_author empty.
- If evidence is ambiguous, unverifiable, or insufficient, abstain with confidence 0.0.
- Only extract foreign title and author when evidence clearly indicates a translation from a foreign language (English, Japanese, German, French, etc.). A known translator or catalog original title is strong evidence.

Field Rules:
- original_title and original_author: original-language forms (not localized forms).
- language: original language name in English (e.g. "English", "Japanese").
- confidence: number from 0.0 to 1.0 (0.0 when abstained).
- abstained: boolean flag.
- rationale: explain the evidence used and why you did or did not abstain.

Return only a JSON object with keys original_title, original_author, language, confidence, abstained, rationale.
