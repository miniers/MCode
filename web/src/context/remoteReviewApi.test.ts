// @ts-ignore Vitest is supplied by the targeted npx test command.
import { describe, expect, it } from 'vitest';
import { reviewContentHash, reviewHunkHash } from './remoteReviewApi';

const lf = String.fromCharCode(10);

describe('review hunk hashes', () => {
  it('matches the desktop review snapshot hashes', async () => {
    const text = ['one', 'TWO', 'three', 'FOUR', 'five', ''].join(lf);
    await expect(reviewContentHash(text)).resolves.toBe(
      '19aa528fb65964f783c0e25358fd35b1b5bd1548b778f4e384f9e5058e3b3bf2'
    );
    await expect(reviewHunkHash(text, 2, 1)).resolves.toBe(
      '465a43c7b7b79945ec5bc4dd80b20230ea1a992bd6401fe2ed5f736d67799e0c'
    );
  });

  it('preserves the final line without a newline', async () => {
    await expect(reviewHunkHash(['one', 'TWO'].join(lf), 2, 1)).resolves.toBe(
      'a1a8a8cbbed4eb53ae62ee4fb0787504087232c29aa4d817757d06b68d0501ca'
    );
  });
});
