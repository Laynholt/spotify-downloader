import asyncio
import os
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import get_token


class FakeInput:
    def __init__(self):
        self.values = []

    async def send_keys(self, value):
        self.values.append(value)


class FakeButton:
    def __init__(self):
        self.clicks = 0

    async def click(self):
        self.clicks += 1


class FakePage:
    def __init__(self, token_after=3):
        self.input = FakeInput()
        self.button = FakeButton()
        self.evaluations = 0
        self.token_after = token_after

    async def select(self, selector):
        if selector == ".searchInput":
            return self.input
        if selector == 'button[type="submit"]':
            return self.button
        return None

    async def evaluate(self, _script):
        self.evaluations += 1
        if self.evaluations >= self.token_after:
            return "eyJ.test-token"
        return None


class SubmitAndWaitForTokenTests(unittest.TestCase):
    def test_clicks_once_and_waits_until_token_appears(self):
        page = FakePage(token_after=4)

        async def no_sleep(_seconds):
            return None

        with patch("get_token.asyncio.sleep", no_sleep):
            token = asyncio.run(get_token.submit_and_wait_for_token(page, max_wait=10))

        self.assertEqual(token, "eyJ.test-token")
        self.assertEqual(page.button.clicks, 1)
        self.assertEqual(
            page.input.values,
            ["https://open.spotify.com/track/53iuhJlwXhSER5J2IYYv1W"],
        )


if __name__ == "__main__":
    unittest.main()
