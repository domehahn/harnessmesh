"""Small dependency-free HarnessMesh dashboard SDK."""
import json
import urllib.parse
import urllib.request

class HarnessMeshError(RuntimeError):
    pass

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
