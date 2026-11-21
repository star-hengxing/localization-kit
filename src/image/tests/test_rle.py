import numpy as np
import pytest

from src.image.rle import decode, encode


def test_coco_order_and_round_trip():
    mask = np.array([[1, 0, 1], [1, 0, 0]], dtype=bool)
    rle = {"size": [2, 3], "counts": [0, 2, 2, 1, 1]}
    assert encode(mask) == rle
    np.testing.assert_array_equal(decode(rle), mask)


@pytest.mark.parametrize("shape", [(0, 0), (0, 7), (7, 0), (3, 5)])
def test_empty_mask(shape):
    mask = np.zeros(shape, dtype=bool)
    np.testing.assert_array_equal(decode(encode(mask)), mask)
    np.testing.assert_array_equal(decode({"size": list(shape), "counts": []}), mask)


def test_inconsistent_runs_are_rejected():
    with pytest.raises(ValueError, match="dimensions"):
        decode({"size": [4, 4], "counts": [2, 16]})
