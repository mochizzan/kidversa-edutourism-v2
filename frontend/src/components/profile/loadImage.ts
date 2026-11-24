/**
 * Decode seam for the photo editor (spec 09): one URL/object-URL →
 * HTMLImageElement. A separate module so the modal's tests can mock the
 * decode wholesale (jsdom has no real image decoder).
 */
export default function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('image decode failed'))
    img.src = src
  })
}
