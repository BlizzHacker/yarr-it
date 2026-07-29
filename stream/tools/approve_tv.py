"""Approve a TV device code without a browser, for automated testing.

The web flow's final stage does exactly this and nothing more:

    token.user = self.request.user
    token.save()

Doing it here removes the one manual step from the loop, so a TV sign-in can be
exercised end to end in CI or from a script. It is an administrative action --
it grants a device access to an account -- so it belongs behind the same trust
boundary as any other Authentik admin operation, and it is not something to
expose to users.

Usage (inside the authentik-server container):
    USER_CODE=123456789 [AS_USER=someone] ak shell -c "exec(open('/tmp/approve_tv.py').read())"
"""

import os

from authentik.core.models import User
from authentik.providers.oauth2.models import DeviceToken

code = os.environ.get("USER_CODE", "").strip()
if not code:
    raise SystemExit("USER_CODE not set")

token = DeviceToken.objects.filter(user_code=code).first()
if token is None:
    raise SystemExit("no device code %r outstanding (expired, or never issued)" % code)
if token.user_id:
    print("ALREADY_APPROVED by %s" % token.user)
    raise SystemExit(0)

wanted = os.environ.get("AS_USER", "").strip()
if wanted:
    user = User.objects.filter(username=wanted).first() or User.objects.filter(email=wanted).first()
    if user is None:
        raise SystemExit("no such user %r" % wanted)
else:
    # Default to a superuser so the happy path needs no argument, but say which
    # one was chosen -- silently binding a TV to an unexpected account is worse
    # than refusing.
    # Authentik has no is_superuser on User; admin status comes from group
    # membership, so the lookup has to go through ak_groups.
    user = (
        User.objects.filter(is_active=True, ak_groups__is_superuser=True)
        .distinct()
        .order_by("pk")
        .first()
    )
    if user is None:
        raise SystemExit("no active superuser to approve as; pass AS_USER")

token.user = user
token.save()
print("APPROVED code=%s as user=%s (%s)" % (code, user.username, user.email or "no email"))
