"""Small dependency-free HarnessMesh dashboard SDK."""
import json
import urllib.request

class HarnessMeshError(RuntimeError):
    pass

class Client:
    def __init__(self, base_url: str, api_key: str):
        self.base_url = base_url.rstrip('/')
        self.api_key = api_key
    def request(self, path: str):
        request = urllib.request.Request(self.base_url + '/api/v1/dashboard' + path,
            headers={'X-HarnessMesh-API-Key': self.api_key, 'Accept': 'application/json'})
        try:
            with urllib.request.urlopen(request) as response:
                return json.loads(response.read())
        except Exception as exc:
            raise HarnessMeshError(str(exc)) from exc
    def summary(self): return self.request('/summary')
    def skills(self): return self.request('/skills')
    def marketplace(self): return self.request('/marketplace')
    def sessions(self): return self.request('/sessions')
    def jobs(self): return self.request('/jobs')
    def approvals(self): return self.request('/approvals')
    def observability_logs(self): return self.request('/observability/logs')
    def observability_traces(self): return self.request('/observability/traces')
