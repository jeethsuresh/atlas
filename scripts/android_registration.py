"""Check Android's package registration before publishing an APK."""
import json
import os
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


def require_registered(package, fingerprint):
    key = os.environ.get("ANDROID_DEVELOPER_ID_STATUS_API_KEY")
    if not key:
        raise ValueError("Set ANDROID_DEVELOPER_ID_STATUS_API_KEY in the android-release environment")

    endpoint = "https://androiddeveloperidstatus.googleapis.com/v1/packages/"
    query = urlencode({"certificateFingerprint": fingerprint})
    url = f"{endpoint}{quote(package, safe='')}/packageRegistrationStatus:check?{query}"
    request = Request(url, headers={"Accept": "application/json", "X-Goog-Api-Key": key})
    try:
        with urlopen(request, timeout=15) as response:
            result = json.load(response)
    except HTTPError as error:
        raise ValueError(f"Android registration check for {package} failed with HTTP {error.code}") from None
    except (URLError, TimeoutError, ValueError):
        raise ValueError(f"Could not read Android registration status for {package}") from None

    if result.get("state") != "REGISTERED":
        raise ValueError(f"Android has not confirmed registration of {package} with its release certificate")
    print(f"Android registration verified for {package}")
