"""Give auth.yarrit.com its own brand.

Authentik picks a brand by matching the hostname the browser asked for. There
has never been an entry for auth.yarrit.com, so every Yarr.It sign-in has been
falling through to the default brand -- which is titled "MoveWeight". That is
the name a TV owner sees on the one page they are sent to when pairing, and it
is the last place the old name was still showing.

Flows are copied from the default brand rather than picked by name: the default
is known to work, and a brand with an unset flow silently breaks the journey it
belongs to.

Idempotent -- safe to run twice.
"""

from authentik.brands.models import Brand

DOMAIN = "auth.yarrit.com"
TITLE = "Yarr.It"

default = Brand.objects.filter(default=True).first()
if default is None:
    raise SystemExit("no default brand to copy flows from -- refusing to guess")

brand, created = Brand.objects.get_or_create(domain=DOMAIN)

brand.branding_title = TITLE
brand.default = False

# Every flow the default brand has, so nothing is left unset. flow_device_code
# is the one that matters here: unset, the /device page has no stage to run and
# the code box never appears.
for field in (
    "flow_authentication",
    "flow_invalidation",
    "flow_recovery",
    "flow_unenrollment",
    "flow_user_settings",
    "flow_device_code",
):
    if hasattr(default, field):
        setattr(brand, field, getattr(default, field))

# Keep whatever logo/favicon the default uses unless Yarr.It has its own; a
# broken image is worse than an inherited one.
for field in ("branding_logo", "branding_favicon"):
    if hasattr(default, field) and not getattr(brand, field, None):
        setattr(brand, field, getattr(default, field))

brand.save()

print(f"  {'created' if created else 'updated'}: {brand.domain} -> {brand.branding_title!r}")
dev = getattr(brand, "flow_device_code", None)
print(f"  device_code_flow = {dev.slug if dev else '*** STILL UNSET ***'}")
auth = getattr(brand, "flow_authentication", None)
print(f"  authentication   = {auth.slug if auth else '(inherits default)'}")

print()
print("  all brands now:")
for b in Brand.objects.all().order_by("domain"):
    print(f"    {b.domain:32} default={str(b.default):5} title={b.branding_title!r}")
