"""Small dependency-free HarnessMesh dashboard and A2A SDK with typed contracts."""
from dataclasses import asdict, dataclass, field
import json
from typing import Any, Dict, List, Optional
import urllib.parse
import urllib.request

class HarnessMeshError(RuntimeError):
    pass

@dataclass(frozen=True)
class DecisionProposal:
    """Typed decision contract for Pydantic AI, LangGraph and Agno agents."""
    space_id: str
    title: str
    summary: str
    evidence_ids: List[str] = field(default_factory=list)
    file_scope: List[str] = field(default_factory=list)

    def __post_init__(self) -> None:
        if not self.space_id.strip() or not self.title.strip() or not self.summary.strip():
            raise ValueError("space_id, title and summary are required")

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)

    def model_dump(self) -> Dict[str, Any]:
        return self.to_dict()

@dataclass(frozen=True)
class ReviewFinding:
    """Typed structured review finding contract."""
    severity: str
    file: str
    title: str
    detail: str
    line: int = 0

    def __post_init__(self) -> None:
        allowed = {"low", "medium", "high", "critical"}
        if self.severity.lower().strip() not in allowed:
            raise ValueError(f"severity must be one of {sorted(allowed)}")
        if not self.title.strip():
            raise ValueError("title is required")

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)

    def model_dump(self) -> Dict[str, Any]:
        return self.to_dict()

@dataclass(frozen=True)
class MemoryBlockPayload:
    """Letta/Cognee-compatible hierarchical memory block payload."""
    tier: str
    label: str
    value: str
    read_only: bool = False

    def __post_init__(self) -> None:
        if self.tier not in {"core", "archival", "recall"}:
            raise ValueError("tier must be core, archival or recall")
        if not self.label.strip():
            raise ValueError("label is required")

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)

    def model_dump(self) -> Dict[str, Any]:
        return self.to_dict()

@dataclass(frozen=True)
class HarnessPatchProposal:
    """Approval-gated recursive harness optimization proposal (Propose-Probe-Compose)."""
    skill: str
    base_version: str
    proposed_version: str
    requires_approval: bool = True
    added_tools: List[str] = field(default_factory=list)
    enable_strict_order: bool = False
    suggested_steps: List[str] = field(default_factory=list)
    findings: List[str] = field(default_factory=list)

    def __post_init__(self) -> None:
        if not self.skill.strip() or not self.base_version.strip() or not self.proposed_version.strip():
            raise ValueError("skill, base_version and proposed_version are required")
        if not self.requires_approval:
            raise ValueError("requires_approval must remain True for harness mutations")

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)

    def model_dump(self) -> Dict[str, Any]:
        return self.to_dict()

class Client:
    def __init__(self, base_url: str, api_key: str):
        self.base_url = base_url.rstrip('/')
        self.api_key = api_key
    def request(self, path: str, method: str = 'GET', payload=None):
        body = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(self.base_url + '/api/v1/dashboard' + path,
            data=body, method=method,
            headers={'X-HarnessMesh-API-Key': self.api_key, 'Accept': 'application/json', 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request) as response:
                return json.loads(response.read())
        except Exception as exc:
            raise HarnessMeshError(str(exc)) from exc
    def summary(self): return self.request('/summary')
    def skills(self): return self.request('/skills')
    def marketplace(self): return self.request('/marketplace')
    def sessions(self): return self.request('/sessions')
    def session(self, session_id: str): return self.request('/sessions/' + urllib.parse.quote(session_id, safe=''))
    def session_replay(self, session_id: str): return self.request('/sessions/' + urllib.parse.quote(session_id, safe='') + '/replay')
    def jobs(self): return self.request('/jobs')
    def job(self, job_id: str): return self.request('/jobs/' + urllib.parse.quote(job_id, safe=''))
    def job_action(self, job_id: str, action: str): return self.request('/jobs/' + urllib.parse.quote(job_id, safe=''), 'POST', {'action': action})
    def approvals(self): return self.request('/approvals')
    def approval(self, approval_id: str): return self.request('/approvals/' + urllib.parse.quote(approval_id, safe=''))
    def decide_approval(self, approval_id: str, status: str): return self.request('/approvals/' + urllib.parse.quote(approval_id, safe=''), 'POST', {'status': status})
    def observability_logs(self): return self.request('/observability/logs')
    def observability_traces(self): return self.request('/observability/traces')
    def submit_a2a_task(self, tenant: str, a2a_token: str, space_id: str, prompt: str, sender: str = 'external-agent', channel: Optional[str] = None):
        if not tenant.strip() or not a2a_token.strip() or not space_id.strip() or not prompt.strip():
            raise ValueError('tenant, a2a_token, space_id and prompt are required')
        metadata: Dict[str, Any] = {'space_id': space_id}
        if channel:
            metadata['channel'] = channel
        body = json.dumps({'sender': sender, 'prompt': prompt, 'metadata': metadata}).encode()
        req = urllib.request.Request(self.base_url + '/a2a/v1/tasks', data=body, method='POST',
            headers={'Authorization': 'Bearer ' + a2a_token, 'X-HarnessMesh-Tenant': tenant, 'Accept': 'application/json', 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req) as resp:
                return json.loads(resp.read())
        except Exception as exc:
            raise HarnessMeshError(str(exc)) from exc
