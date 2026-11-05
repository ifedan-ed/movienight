"""OpenAI-compatible chat client for the install's model endpoint (LiteLLM
or any OpenAI-style API). The model is set in the environment."""
from __future__ import annotations

import json
import logging
import re
from typing import Any

import httpx

log = logging.getLogger("movienight.ai")


class AIError(Exception):
    pass


class AIOutOfRoom(AIError):
    """The model stopped at max_tokens before its answer was whole (a
    reasoning model can think the whole allowance away)."""


def parse_json(content: str) -> Any:
    """The reply as JSON; tolerates a ```json fence or text around the object."""
    text = (content or "").strip()
    if not text:
        raise ValueError("empty reply")
    fence = re.search(r"```(?:json)?\s*(.*?)```", text, re.S)
    if fence:
        text = fence.group(1).strip()
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        start, end = text.find("{"), text.rfind("}")
        if start >= 0 and end > start:
            return json.loads(text[start:end + 1])
        raise


class LiteLLMClient:
    def __init__(self, base_url: str, api_key: str, model: str, max_tokens: int = 8000, client: httpx.AsyncClient | None = None):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.model = model
        self.max_tokens = max_tokens
        self._client = client or httpx.AsyncClient(timeout=180.0)

    @property
    def enabled(self) -> bool:
        return bool(self.api_key and self.model)

    async def chat_json(self, system: str, user: str, attempts: int = 2) -> Any:
        """One chat completion, parsed as JSON. Reasoning models (DeepSeek)
        can spend the whole budget thinking or return broken JSON now and
        then, so a bad reply is retried once before giving up."""
        if not self.enabled:
            raise AIError("No AI model is set up on this install")
        last: Exception | None = None
        out_of_room = False
        for attempt in range(attempts):
            resp = await self._client.post(
                f"{self.base_url}/chat/completions",
                headers={"Authorization": f"Bearer {self.api_key}"},
                json={
                    "model": self.model,
                    "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
                    "response_format": {"type": "json_object"},
                    "max_tokens": self.max_tokens,
                    # LiteLLM's response cache would hand back yesterday's rows.
                    "cache": {"no-cache": True},
                },
                timeout=180.0,
            )
            if resp.status_code >= 400:
                raise AIError(f"model endpoint answered {resp.status_code}")
            choice = (resp.json().get("choices") or [{}])[0]
            try:
                return parse_json((choice.get("message") or {}).get("content") or "")
            except ValueError as exc:  # JSONDecodeError is a ValueError
                last = exc
                out_of_room = choice.get("finish_reason") == "length"
                log.warning("model reply unusable (attempt %d, finish=%s): %s", attempt + 1, choice.get("finish_reason"), exc)
        if out_of_room:
            raise AIOutOfRoom(f"the model ran out of room ({self.max_tokens} output tokens) before it answered")
        raise AIError(f"model returned no usable JSON ({last})")
