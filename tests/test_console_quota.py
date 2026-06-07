import asyncio
import tempfile
from pathlib import Path
from types import SimpleNamespace

from app.control.account.backends.local import LocalAccountRepository
from app.control.account.commands import AccountPatch, AccountUpsert
from app.control.account.enums import QuotaSource
from app.control.account.models import AccountRecord, QuotaWindow
from app.control.account.quota_defaults import (
    default_quota_window,
    normalize_quota_window,
    supported_mode_ids,
    supports_mode,
)
from app.control.account.refresh import AccountRefreshService
from app.platform.startup.migration import _backfill_console_quota


def test_console_supported_only_for_basic_pool():
    assert supports_mode("basic", 5)
    assert 5 in supported_mode_ids("basic")

    assert not supports_mode("super", 5)
    assert 5 not in supported_mode_ids("super")

    assert not supports_mode("heavy", 5)
    assert 5 not in supported_mode_ids("heavy")


def test_normalize_console_repairs_zero_without_reset():
    broken = QuotaWindow(
        remaining=0,
        total=0,
        window_seconds=0,
        reset_at=None,
        synced_at=None,
        source=QuotaSource.ESTIMATED,
    )

    repaired = normalize_quota_window("basic", 5, broken)

    assert repaired == default_quota_window("basic", 5)


def test_console_local_use_repairs_broken_quota_then_decrements():
    async def run():
        broken = QuotaWindow(
            remaining=0,
            total=0,
            window_seconds=0,
            reset_at=None,
            synced_at=None,
            source=QuotaSource.ESTIMATED,
        )
        record = AccountRecord(
            token="token-a",
            pool="basic",
            quota={"console": broken.to_dict()},
        )
        repo = _MemoryRepo(record)
        svc = AccountRefreshService(repo)

        await svc.refresh_call_async("token-a", 5)

        console = repo.record.quota_set().console
        assert console is not None
        assert console.remaining == 29
        assert console.total == 30
        assert console.window_seconds == 900
        assert repo.record.usage_use_count == 1

    asyncio.run(run())


def test_bulk_quota_refresh_excludes_local_console_mode(monkeypatch):
    async def run():
        captured = {}

        async def fake_fetch_all_quotas(token, mode_ids):
            captured["token"] = token
            captured["mode_ids"] = mode_ids
            return None

        import app.dataplane.reverse.protocol.xai_usage as xai_usage

        monkeypatch.setattr(xai_usage, "fetch_all_quotas", fake_fetch_all_quotas)

        svc = AccountRefreshService(SimpleNamespace())
        result = await svc._fetch_all_quotas("token-a", "basic")

        assert result is None
        assert captured == {"token": "token-a", "mode_ids": (1,)}

    asyncio.run(run())


def test_console_backfill_repairs_persisted_zero_quota():
    async def run():
        with tempfile.TemporaryDirectory() as tmp:
            repo = LocalAccountRepository(Path(tmp) / "accounts.db")
            repo._init_sync()
            await repo.upsert_accounts([AccountUpsert(token="token-a", pool="basic")])
            broken = QuotaWindow(
                remaining=0,
                total=0,
                window_seconds=0,
                reset_at=None,
                synced_at=None,
                source=QuotaSource.ESTIMATED,
            )
            await repo.patch_accounts(
                [AccountPatch(token="token-a", quota_console=broken.to_dict())]
            )

            await _backfill_console_quota(repo)

            record = (await repo.get_accounts(["token-a"]))[0]
            assert record.quota_set().console == default_quota_window("basic", 5)

    asyncio.run(run())


class _MemoryRepo:
    def __init__(self, record: AccountRecord) -> None:
        self.record = record

    async def get_accounts(self, tokens):
        if self.record.token in tokens:
            return [self.record]
        return []

    async def patch_accounts(self, patches):
        patch = patches[0]
        quota = dict(self.record.quota)
        if patch.quota_console is not None:
            quota["console"] = patch.quota_console
        self.record = self.record.model_copy(
            update={
                "quota": quota,
                "usage_use_count": self.record.usage_use_count
                + (patch.usage_use_delta or 0),
            }
        )
        return None
