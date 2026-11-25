"""Run the Python reference implementation (miyako/4D fetch_dependencies.py) against a mock provider.

Used only by the differential tests: it points the reference at a local mock server and removes sleeps. The
reference script itself is loaded unmodified from REF_SCRIPT.
"""
import importlib.util
import os
import re
import sys
import urllib.parse

path = os.environ["REF_SCRIPT"]
mock = os.environ["MOCK_URL"].rstrip("/")
spec = importlib.util.spec_from_file_location("fetch_dependencies", path)
fd = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fd)

fd.API = mock
netloc = urllib.parse.urlsplit(mock).netloc
fd.time.sleep = lambda *_: None


def headers(self, url, accept):
    h = {"Accept": accept, "User-Agent": fd.USER_AGENT, "X-GitHub-Api-Version": "2022-11-28"}
    if self.token and urllib.parse.urlsplit(url).netloc == netloc:
        h["Authorization"] = "Bearer " + self.token
    return h


fd.GitHub.headers = headers
original = fd.GitHub._request


def request(self, url, accept):
    url = re.sub(r"^https://[^/]+(/api/v4/)", lambda m: mock + m.group(1), url)
    return original(self, url, accept)


fd.GitHub._request = request

if __name__ == "__main__":
    sys.exit(fd.main(sys.argv[1:]))
