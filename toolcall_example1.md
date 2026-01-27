import uvicorn
from fastapi import FastAPI, HTTPException, BackgroundTasks, Request
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel
from typing import List, Optional, Union, Literal, Dict, Any
import time
import base64
import httpx
import uuid
import io
import re
import json
import asyncio
import threading
import queue
from h2ogpte import H2OGPTE

# ================= Configuration =================
HOST = "0.0.0.0"
PORT = 8000
H2O_ADDRESS = "https://h2ogpte.genai.h2o.ai"
H2O_API_KEY = "sk-Qfr37bYcSs65yAEGB4cfMIRM5btvCV5ONOEldeifKbgy3GbX"

MODEL_REDIRECTS = {
    "claude-haiku-4-5-20251001": "claude-sonnet-4-5-20250929",
    "gpt-3.5-turbo": "gpt-4o-mini"
}
# ===============================================

app = FastAPI(title="H2O Ultimate Gateway V2")

print(f"Connecting to H2O at {H2O_ADDRESS}...")
try:
    client = H2OGPTE(address=H2O_ADDRESS, api_key=H2O_API_KEY)
    print("✅ H2O Client Connected!")
except Exception as e:
    print(f"❌ Connection Failed: {e}")
    client = None

CACHED_MODELS: Dict[str, Dict] = {}
LAST_CACHE_TIME = 0
CACHE_TTL = 300

# --- Helpers ---
async def download_image(url: str) -> bytes:
    if url.startswith("data:image"):
        header, encoded = url.split(",", 1)
        return base64.b64decode(encoded)
    async with httpx.AsyncClient() as http_client:
        resp = await http_client.get(url, timeout=30)
        resp.raise_for_status()
        return resp.content

def cleanup_collection(col_id: str):
    try:
        if client: client.delete_collections([col_id])
    except: pass

def refresh_models():
    global CACHED_MODELS, LAST_CACHE_TIME
    if time.time() - LAST_CACHE_TIME < CACHE_TTL and CACHED_MODELS: return CACHED_MODELS
    if not client: return {}
    try:
        llms = client.get_llms()
        new_cache = {m['id']: {"image": m.get("image", False) or m.get("actually_vision", False)} for m in llms}
        CACHED_MODELS = new_cache
        LAST_CACHE_TIME = time.time()
        return CACHED_MODELS
    except: return CACHED_MODELS

def route_model(req_model: str, has_images: bool):
    """Pick a compatible model, always falling back so we don't stream empty responses."""
    if req_model in MODEL_REDIRECTS:
        req_model = MODEL_REDIRECTS[req_model]

    models_map = refresh_models()
    if req_model in models_map:
        if has_images and not models_map[req_model].get("image"):
            return "gpt-4o"
        return req_model

    for m_id in models_map:
        if req_model in m_id or m_id in req_model:
            return m_id

    # Final safety: always return a model instead of None
    return "gpt-4o"

# --- Models ---
class AnthropicTool(BaseModel):
    name: str
    description: Optional[str] = ""
    input_schema: Dict[str, Any]

class AnthropicMessageRequest(BaseModel):
    model: str
    messages: List[Dict[str, Any]]
    system: Optional[Union[str, List[Dict[str, Any]]]] = None
    max_tokens: Optional[int] = 1024
    tools: Optional[List[AnthropicTool]] = None
    stream: Optional[bool] = False

# --- Logic ---
TOOL_SYSTEM_PROMPT_TEMPLATE = """
You have access to the following tools.
To use a tool, you MUST format your response EXACTLY as a JSON block wrapped in <tool_code> tags.
DO NOT add any other text before or after the tool code if you are using a tool.

Available Tools:
{tools_desc}

Format required:
<tool_code>
{{
  "name": "tool_name",
  "input": {{ "param": "value" }}
}}
</tool_code>
"""

def process_system_prompt(system_input: Union[str, List[Dict], None]) -> str:
    if not system_input: return ""
    if isinstance(system_input, str): return system_input
    if isinstance(system_input, list):
        return "\n".join([part.get("text", "") for part in system_input if part.get("type") == "text"])
    return ""

def inject_tools_instruction(system_prompt: str, tools: List[AnthropicTool]) -> str:
    tools_desc = json.dumps([t.model_dump() for t in tools], indent=2)
    instruction = TOOL_SYSTEM_PROMPT_TEMPLATE.format(tools_desc=tools_desc)
    return (system_prompt + "\n\n" + instruction) if system_prompt else instruction

def parse_tool_response(content: str) -> Dict[str, Any]:
    match = re.search(r"<tool_code>\s*({.*?})\s*</tool_code>", content, re.DOTALL)
    if match:
        try:
            payload = json.loads(match.group(1))
            return {
                "is_tool": True,
                "name": payload.get("name"),
                "input": payload.get("input", {})
            }
        except Exception:
            pass
    return {"is_tool": False}

async def anthropic_stream_generator(content_queue: queue.Queue, model: str, msg_id: str):
    # 1. Start
    yield f"event: message_start\ndata: {json.dumps({'type': 'message_start', 'message': {'id': msg_id, 'type': 'message', 'role': 'assistant', 'model': model, 'content': [], 'stop_reason': None, 'stop_sequence': None, 'usage': {'input_tokens': 0, 'output_tokens': 0}}})}\n\n"
    yield f"event: content_block_start\ndata: {json.dumps({'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': ''}})}\n\n"

    full_content = ""
    last_activity = time.time()

    # 3. Stream Loop
    while True:
        try:
            chunk = content_queue.get_nowait()
            if chunk is None: break # End of stream

            # Simple metadata filter
            if '{"isNewTopic":' in chunk: continue

            full_content += chunk
            yield f"event: content_block_delta\ndata: {json.dumps({'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'text_delta', 'text': chunk}})}\n\n"
            last_activity = time.time()

            # Allow event loop to breathe
            await asyncio.sleep(0.001)

        except queue.Empty:
            # Heartbeat to keep connection alive
            if time.time() - last_activity > 15:
                yield ": keep-alive\n\n"
                last_activity = time.time()
            await asyncio.sleep(0.05)

    # 4. Stop
    yield f"event: content_block_stop\ndata: {json.dumps({'type': 'content_block_stop', 'index': 0})}\n\n"

    # 5. Tool Check
    tool_call = parse_tool_response(full_content)
    stop_reason = "end_turn"

    if tool_call.get("is_tool"):
        stop_reason = "tool_use"
        tool_index = 1
        t_block = {
            "type": "tool_use",
            "id": f"toolu_{uuid.uuid4().hex[:10]}",
            "name": tool_call["name"],
            "input": tool_call["input"]
        }
        yield f"event: content_block_start\ndata: {json.dumps({'type': 'content_block_start', 'index': tool_index, 'content_block': t_block})}\n\n"
        yield f"event: content_block_stop\ndata: {json.dumps({'type': 'content_block_stop', 'index': tool_index})}\n\n"

    # 6. Finalize
    yield f"event: message_delta\ndata: {json.dumps({'type': 'message_delta', 'delta': {'stop_reason': stop_reason, 'usage': {'output_tokens': len(full_content)//4}}})}\n\n"
    yield f"event: message_stop\ndata: {json.dumps({'type': 'message_stop'})}\n\n"

# ================= Endpoints =================

@app.post("/v1/messages")
@app.post("/v1/v1/messages")
@app.post("/messages")
async def anthropic_chat(request: AnthropicMessageRequest, bg_tasks: BackgroundTasks):
    col_id = None
    try:
        if not client:
            return JSONResponse(status_code=500, content={"type": "error", "error": {"type": "api_error", "message": "H2O client not initialized"}})

        final_system = process_system_prompt(request.system)
        if request.tools: final_system = inject_tools_instruction(final_system, request.tools)

        images, text_parts = [], []
        for msg in request.messages:
            role = msg.get("role", "user")
            content = msg.get("content", "")
            text_parts.append(f"{role.upper()}: ")
            if isinstance(content, str): text_parts.append(content)
            elif isinstance(content, list):
                for part in content:
                    if part.get("type") == "text": text_parts.append(part.get("text", ""))
                    elif part.get("type") == "image":
                        try:
                            src = part.get("source", {})
                            data = base64.b64decode(src["data"])
                            fname = f"img_{uuid.uuid4().hex[:6]}.png"
                            images.append((fname, data))
                            text_parts.append(f"[Image: {fname}]")
                        except:
                            pass
                    elif part.get("type") == "tool_result":
                        text_parts.append(f"\n[Tool Result]: {part.get('content', '')}\n")
            text_parts.append("\n")

        full_query = "".join(text_parts)
        sess_id, llm_args = None, {}
        rag_config = {"rag_type": "llm_only"}

        if images:
            col_id = client.create_collection(name=f"Vis_{uuid.uuid4().hex[:6]}", description="T")
            client.ingest_uploads(col_id, [client.upload(n, io.BytesIO(d)) for n, d in images])
            sess_id = client.create_chat_session(collection_id=col_id)
            bg_tasks.add_task(cleanup_collection, col_id)
            llm_args["enable_vision"] = "on"
            rag_config["rag_type"] = "rag"
        else:
            sess_id = client.create_chat_session()

        target_model = route_model(request.model, bool(images))
        if not target_model:
            return JSONResponse(status_code=400, content={"type": "error", "error": {"type": "invalid_request_error", "message": f"Model '{request.model}' not available"}})

        print(f"🚀 {request.model} -> {target_model} | Stream: {request.stream}")

        msg_id = f"msg_{uuid.uuid4().hex}"

        # --- STREAMING ---
        if request.stream:
            content_q = queue.Queue()

            # Using a list to track state across threads simply
            state = {"has_partial": False, "pushed_final": False, "final_content": ""}

            def callback(p):
                # Filter for Partials to avoid duplication
                if p.__class__.__name__ == 'PartialChatMessage':
                    if p.content:
                        content_q.put(p.content)
                        state["has_partial"] = True
                # Capture the final message so we can fall back if partials never arrive
                elif p.__class__.__name__ == 'ChatMessage':
                    state["final_content"] = p.content or ""
                    if not state["has_partial"] and state["final_content"]:
                        print("⚠️ No partials received, using final content.")
                        content_q.put(state["final_content"])
                        state["pushed_final"] = True

            def h2o_worker():
                try:
                    with client.connect(sess_id) as session:
                        reply = session.query(
                            message=full_query.strip(),
                            llm=target_model,
                            llm_args=llm_args,
                            rag_config=rag_config,
                            system_prompt=final_system,
                            callback=callback, # Smart callback
                            timeout=120
                        )
                        if not state["has_partial"] and not state["pushed_final"]:
                            fallback = state["final_content"] or getattr(reply, "content", "") or ""
                            if fallback:
                                print("⚠️ Injecting fallback final content.")
                                content_q.put(fallback)
                except Exception as e:
                    print(f"H2O Worker Error: {e}")
                    content_q.put(f"[Gateway Error] {e}")
                finally:
                    content_q.put(None)

            threading.Thread(target=h2o_worker, daemon=True).start()

            return StreamingResponse(
                anthropic_stream_generator(content_q, target_model, msg_id),
                media_type="text/event-stream"
            )

        # --- BLOCKING ---
        with client.connect(sess_id) as session:
            reply = session.query(
                message=full_query.strip(),
                llm=target_model,
                llm_args=llm_args,
                rag_config=rag_config,
                system_prompt=final_system,
                timeout=120
            )

        content = reply.content or " "
        tp = parse_tool_response(content)
        resp_content = []
        stop_reason = "end_turn"
        if tp.get("is_tool"):
            stop_reason = "tool_use"
            resp_content.append({"type": "tool_use", "id": f"toolu_{uuid.uuid4().hex[:10]}", "name": tp["name"], "input": tp["input"]})
        else:
            resp_content.append({"type": "text", "text": content})

        return {"id": msg_id, "type": "message", "role": "assistant", "content": resp_content, "model": request.model, "stop_reason": stop_reason, "usage": {"input_tokens": 0, "output_tokens": 0}}

    except Exception as e:
        print(f"❌ Error: {e}")
        if col_id: cleanup_collection(col_id)
        return JSONResponse(status_code=500, content={"type": "error", "error": {"type": "api_error", "message": str(e)}})

if __name__ == "__main__":
    uvicorn.run(app, host=HOST, port=PORT)
