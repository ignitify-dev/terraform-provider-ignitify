# A tiny task for the provider's acceptance tests.
VERSION = 1


def run(payload, ctx):
    return {"version": VERSION, "echo": payload}
