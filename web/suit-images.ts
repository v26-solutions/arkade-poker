// Small, font-independent suit images for the existing Kitty graphics renderer.
// Keep suit order and ID encoding in sync with internal/ui/suit_images.go.
const paths = [
  'M50 8 C34 8 27 25 35 37 C18 29 5 43 9 58 C13 74 33 77 44 64 C44 78 38 86 29 92 L71 92 C62 86 56 78 56 64 C67 77 87 74 91 58 C95 43 82 29 65 37 C73 25 66 8 50 8 Z',
  'M50 5 L93 50 L50 95 L7 50 Z',
  'M50 91 C39 79 7 57 7 32 C7 7 37 1 50 24 C63 1 93 7 93 32 C93 57 61 79 50 91 Z',
  'M50 5 C39 20 7 43 7 62 C7 82 33 86 44 70 C44 80 39 87 29 93 L71 93 C61 87 56 80 56 70 C67 86 93 82 93 62 C93 43 61 20 50 5 Z',
];

interface ImageTerminal {
  write(data: string, callback: () => void): void;
}

export async function loadSuitImages(terminal: ImageTerminal): Promise<void> {
  const canvas = document.createElement('canvas');
  canvas.width = 96;
  canvas.height = 160;
  const ctx = canvas.getContext('2d')!;
  // Image storage belongs to a screen. The poker UI uses the alternate screen,
  // so install assets there rather than in the normal terminal buffer.
  const commands: string[] = ['\x1b[?1049h'];
  for (let suit = 0; suit < paths.length; suit++) {
    for (const color of [0x7cff00, 0x95b77c]) {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      ctx.save();
      ctx.translate(2, 34);
      ctx.scale(0.92, 0.92);
      ctx.fillStyle = '#' + color.toString(16).padStart(6, '0');
      ctx.fill(new Path2D(paths[suit]));
      ctx.restore();
      const data = canvas.toDataURL('image/png').split(',')[1];
      const id = ((suit + 1) << 24) | color;
      // q=2 avoids replies entering Bubble Tea's keyboard input. U=1 creates
      // a virtual placement; only the cells emitted by the UI show the image.
      for (let offset = 0; offset < data.length; offset += 4096) {
        const more = offset + 4096 < data.length ? 1 : 0;
        const options = offset === 0 ? `a=T,f=100,q=2,U=1,i=${id},c=3,r=3,` : '';
        commands.push(`\x1b_G${options}m=${more};${data.slice(offset, offset + 4096)}\x1b\\`);
      }
    }
  }
  await new Promise<void>(resolve => terminal.write(commands.join(''), resolve));
}
