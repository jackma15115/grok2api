import asyncio
import json
import pathlib
import tomllib
from types import SimpleNamespace

import orjson

from app.dataplane.reverse.protocol import xai_console_chat
from app.dataplane.reverse.protocol.tool_parser import parse_tool_calls
from app.products.anthropic import console_messages
from app.products.openai import console_chat, console_responses


TOOL_DEF = {
    "type": "function",
    "function": {
        "name": "lookup",
        "description": "Look up a value.",
        "parameters": {
            "type": "object",
            "properties": {"query": {"type": "string"}},
            "required": ["query"],
        },
    },
}

ANTHROPIC_TOOL_DEF = {
    "name": "lookup",
    "description": "Look up a value.",
    "input_schema": {
        "type": "object",
        "properties": {"query": {"type": "string"}},
        "required": ["query"],
    },
}

TOOL_XML = (
    "<tool_calls>"
    "<tool_call>"
    "<tool_name>lookup</tool_name>"
    '<parameters>{"query":"weather"}</parameters>'
    "</tool_call>"
    "</tool_calls>"
)

TOOL_XML_ALT = (
    "<tool_calls>"
    "<tool_call>"
    "<tool>lookup</tool>"
    '<tool_input>{"query":"weather"}</tool_input>'
    "</tool_call>"
    "</tool_calls>"
)


class DummyConfig:
    def __init__(self, *, console_tool_call=True, console_native_tools=False):
        self.console_tool_call = console_tool_call
        self.console_native_tools = console_native_tools

    def get_bool(self, key, default=False):
        if key == "features.console_tool_call":
            return self.console_tool_call
        if key == "features.console_native_tools":
            return self.console_native_tools
        return default

    def get_float(self, key, default=0.0):
        return default

    def get(self, key, default=None):
        if key == "retry.on_codes":
            return "429,401,503"
        if key == "chat.retry_on_codes":
            return None
        return default


class DummyDirectory:
    async def release(self, acct):
        return None

    async def feedback(self, token, kind, selected_mode_id, now_s_val=None):
        return None


async def _reserve_account(directory, spec, now_s_override=None, exclude_tokens=None):
    return SimpleNamespace(token="token"), 5


async def _noop(*args, **kwargs):
    return None


def _event_text(delta):
    return "response.output_text.delta", orjson.dumps({"delta": delta}).decode()


def _event_completed(input_tokens=11, output_tokens=7):
    return "response.completed", orjson.dumps({
        "response": {
            "usage": {
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "total_tokens": input_tokens + output_tokens,
            }
        }
    }).decode()


def _install_common_mocks(monkeypatch, module, *, events, config=None, payloads=None):
    payloads = payloads if payloads is not None else []
    config = config or DummyConfig()

    async def fake_stream_console_chat(token, payload, *, timeout_s=120.0):
        payloads.append(payload)
        for event in events:
            yield event

    monkeypatch.setattr(module, "get_config", lambda: config)
    monkeypatch.setattr(module, "resolve_model", lambda model: SimpleNamespace(name=model))
    monkeypatch.setattr(module, "reserve_account", _reserve_account)
    monkeypatch.setattr(module, "_quota_sync", _noop)
    monkeypatch.setattr(module, "_fail_sync", _noop)
    monkeypatch.setattr(module, "stream_console_chat", fake_stream_console_chat)

    if hasattr(module, "estimate_prompt_tokens"):
        monkeypatch.setattr(module, "estimate_prompt_tokens", lambda value, **kwargs: 11)
    if hasattr(module, "estimate_tokens"):
        monkeypatch.setattr(module, "estimate_tokens", lambda value: 7)
    if hasattr(module, "estimate_tool_call_tokens"):
        monkeypatch.setattr(module, "estimate_tool_call_tokens", lambda value: 5)

    import app.products.openai._format as openai_format

    monkeypatch.setattr(openai_format, "estimate_prompt_tokens", lambda value, **kwargs: 11)
    monkeypatch.setattr(openai_format, "estimate_tokens", lambda value: 7)
    monkeypatch.setattr(openai_format, "estimate_tool_call_tokens", lambda value: 5)

    import app.dataplane.account as account_module

    monkeypatch.setattr(account_module, "_directory", DummyDirectory())
    return payloads


def _data_frames(sse_frames):
    frames = []
    for frame in sse_frames:
        if "\ndata: " in frame:
            data = frame.split("\ndata: ", 1)[1].strip()
        elif frame.startswith("data: "):
            data = frame[6:].strip()
        else:
            continue
        if data == "[DONE]":
            frames.append(data)
        else:
            frames.append(orjson.loads(data))
    return frames


async def _collect_async_iter(async_iterable):
    return [item async for item in async_iterable]


def test_config_defaults_keep_console_tool_call_off_and_native_tools_on():
    data = tomllib.loads(
        (pathlib.Path(__file__).resolve().parents[1] / "config.defaults.toml").read_text(
            encoding="utf-8"
        )
    )

    assert data["features"]["console_tool_call"] is False
    assert data["features"]["console_native_tools"] is True


def test_console_payload_native_tool_switch_and_tool_history():
    messages = [
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [{
                "id": "call_1",
                "type": "function",
                "function": {"name": "lookup", "arguments": '{"query":"weather"}'},
            }],
        },
        {"role": "tool", "tool_call_id": "call_1", "content": "sunny"},
    ]

    payload = xai_console_chat.build_console_payload(
        messages=messages,
        model="grok-4.3-console",
        inject_native_tools=False,
    )
    assert "tools" not in payload
    assert "<tool_calls>" in payload["input"][0]["content"][0]["text"]
    assert "[tool result for call_1]:\nsunny" == payload["input"][1]["content"][0]["text"]

    payload_with_native = xai_console_chat.build_console_payload(
        messages=[{"role": "user", "content": "hi"}],
        model="grok-4.3-console",
        inject_native_tools=True,
    )
    assert payload_with_native["tool_choice"] == "auto"
    assert {tool["type"] for tool in payload_with_native["tools"]} == {
        "web_search",
        "x_search",
    }


def test_chat_non_stream_returns_tool_calls_when_enabled(monkeypatch):
    payloads = _install_common_mocks(
        monkeypatch,
        console_chat,
        events=[_event_text(TOOL_XML), _event_completed()],
    )

    result = asyncio.run(console_chat.completions(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=False,
        tools=[TOOL_DEF],
        tool_choice="required",
    ))

    assert "tools" not in payloads[0]
    assert result["choices"][0]["finish_reason"] == "tool_calls"
    tool_call = result["choices"][0]["message"]["tool_calls"][0]
    assert tool_call["function"]["name"] == "lookup"
    assert orjson.loads(tool_call["function"]["arguments"]) == {"query": "weather"}


def test_tool_parser_accepts_console_xml_variants():
    result = parse_tool_calls(TOOL_XML_ALT, ["lookup"])

    assert len(result.calls) == 1
    assert result.calls[0].name == "lookup"
    assert orjson.loads(result.calls[0].arguments) == {"query": "weather"}


def test_chat_stream_buffers_split_tool_xml_and_emits_no_text_finish(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_chat,
        events=[
            _event_text("<tool"),
            _event_text("_calls><tool_call><tool_name>lookup</tool_name>"),
            _event_text('<parameters>{"query":"weather"}</parameters></tool_call></tool_calls>'),
        ],
    )

    stream = asyncio.run(console_chat.completions(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=True,
        tools=[TOOL_DEF],
        tool_choice="required",
    ))
    frames = _data_frames(asyncio.run(_collect_async_iter(stream)))

    assert frames[-1] == "[DONE]"
    chunks = [frame for frame in frames if isinstance(frame, dict)]
    assert chunks[0]["choices"][0]["delta"]["tool_calls"][0]["function"]["name"] == "lookup"
    assert chunks[1]["choices"][0]["finish_reason"] == "tool_calls"
    assert not any(
        frame.get("choices", [{}])[0].get("finish_reason") == "stop"
        for frame in chunks
    )


def test_responses_stream_pure_tool_call_has_no_dangling_message_item(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_responses,
        events=[_event_text(TOOL_XML)],
    )

    stream = asyncio.run(console_responses.create(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=True,
        emit_think=False,
        temperature=0.7,
        top_p=0.95,
        response_id="resp_test",
        reasoning_id="rs_test",
        message_id="msg_test",
        tools=[TOOL_DEF],
        tool_choice="required",
    ))
    frames = asyncio.run(_collect_async_iter(stream))

    assert not any('"type":"message"' in frame for frame in frames)
    assert any('"type":"function_call"' in frame for frame in frames)
    assert any('"response.completed"' in frame for frame in frames)


def test_responses_non_stream_returns_function_call_item(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_responses,
        events=[_event_text(TOOL_XML), _event_completed()],
    )

    result = asyncio.run(console_responses.create(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=False,
        emit_think=False,
        temperature=0.7,
        top_p=0.95,
        response_id="resp_test",
        reasoning_id="rs_test",
        message_id="msg_test",
        tools=[TOOL_DEF],
        tool_choice="required",
    ))

    assert result["output"][0]["type"] == "function_call"
    assert result["output"][0]["name"] == "lookup"
    assert orjson.loads(result["output"][0]["arguments"]) == {"query": "weather"}


def test_anthropic_stream_pure_tool_call_has_no_empty_text_block(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_messages,
        events=[_event_text(TOOL_XML)],
    )

    stream = asyncio.run(console_messages.create(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=True,
        emit_think=False,
        temperature=0.7,
        top_p=0.95,
        msg_id="msg_test",
        tools=[ANTHROPIC_TOOL_DEF],
        tool_choice={"type": "any"},
    ))
    frames = asyncio.run(_collect_async_iter(stream))

    assert not any('"type":"text"' in frame for frame in frames)
    assert any('"type":"tool_use"' in frame for frame in frames)
    assert any('"stop_reason":"tool_use"' in frame for frame in frames)


def test_anthropic_non_stream_returns_tool_use(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_messages,
        events=[_event_text(TOOL_XML), _event_completed()],
    )

    result = asyncio.run(console_messages.create(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=False,
        emit_think=False,
        temperature=0.7,
        top_p=0.95,
        msg_id="msg_test",
        tools=[ANTHROPIC_TOOL_DEF],
        tool_choice={"type": "any"},
    ))

    assert result["stop_reason"] == "tool_use"
    assert result["content"][0]["type"] == "tool_use"
    assert result["content"][0]["name"] == "lookup"
    assert result["content"][0]["input"] == {"query": "weather"}


def test_console_tool_call_disabled_leaves_output_as_text(monkeypatch):
    _install_common_mocks(
        monkeypatch,
        console_chat,
        events=[_event_text(TOOL_XML), _event_completed()],
        config=DummyConfig(console_tool_call=False, console_native_tools=True),
    )

    result = asyncio.run(console_chat.completions(
        model="grok-4.3-console",
        messages=[{"role": "user", "content": "call lookup"}],
        stream=False,
        tools=[TOOL_DEF],
        tool_choice="required",
    ))

    assert result["choices"][0]["finish_reason"] == "stop"
    assert result["choices"][0]["message"]["content"] == TOOL_XML
